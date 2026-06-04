// Command sttdebug exercises the per-speaker transcription path in isolation
// with verbose instrumentation. It exists to debug the intermittent
// "0 words from N seconds of audio" failures observed both in CI e2e runs
// and on real Talk recordings (sparse/DTX per-participant tracks).
//
// Usage:
//
//	sttdebug <recording.mkv> <stream-index>
package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"gocassini/internal/transcribe"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sttdebug <recording.mkv> <stream-index>")
		os.Exit(2)
	}
	mkv := os.Args[1]
	wantIndex, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad stream index:", err)
		os.Exit(2)
	}

	streams, durMS, err := transcribe.ProbeMKV(mkv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
	fmt.Printf("recording duration: %.1fs, %d audio streams\n", float64(durMS)/1000, len(streams))

	var stream *transcribe.AudioStream
	for i := range streams {
		s := &streams[i]
		fmt.Printf("  stream %d: %s (start %dms)\n", s.Index, s.SpeakerLabel, s.StartTimeMS)
		if s.Index == wantIndex {
			stream = s
		}
	}
	if stream == nil {
		fmt.Fprintln(os.Stderr, "stream index not found among audio streams")
		os.Exit(1)
	}

	samples, err := transcribe.ExtractSpeakerFloats(mkv, *stream)
	if err != nil {
		fmt.Fprintln(os.Stderr, "extract:", err)
		os.Exit(1)
	}
	fmt.Printf("extracted %.1fs of audio for %s\n", float64(len(samples))/16000, stream.SpeakerLabel)

	if wavOut := os.Getenv("STTDEBUG_DUMP_WAV"); wavOut != "" {
		if err := writeWav16kMono(wavOut, samples); err != nil {
			fmt.Fprintln(os.Stderr, "dump wav:", err)
			os.Exit(1)
		}
		fmt.Printf("dumped extracted samples to %s\n", wavOut)
	}

	cacheDir := os.Getenv("CASSINI_CACHE_ROOT")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = home + "/.cache/cassini"
	}
	modelPaths, err := transcribe.EnsureModel(cacheDir, transcribe.DefaultModelID(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "model:", err)
		os.Exit(1)
	}
	vadPath, err := transcribe.EnsureVAD(cacheDir, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vad:", err)
		os.Exit(1)
	}

	// STTDEBUG_USE_PACKAGE=1 exercises the production transcribe.Recognizer
	// path instead of the hand-rolled instrumentation below.
	if os.Getenv("STTDEBUG_USE_PACKAGE") != "" {
		prec, err := transcribe.NewRecognizer(modelPaths, vadPath, "cpu", 8)
		if err != nil {
			fmt.Fprintln(os.Stderr, "recognizer:", err)
			os.Exit(1)
		}
		defer prec.Close()
		words, err := prec.Transcribe(samples, modelPaths.SampleRate, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, "transcribe:", err)
			os.Exit(1)
		}
		fmt.Printf("package path: %d words\n", len(words))
		for _, w := range words {
			fmt.Printf("  %8.2fs-%8.2fs %s\n", float64(w.StartMS)/1000, float64(w.EndMS)/1000, w.Text)
		}
		return
	}

	// Recreate the recognizer + VAD with the same settings as
	// transcribe.NewRecognizer, but drive the loop ourselves so we can dump
	// per-segment diagnostics.
	cfg := sherpa.OfflineRecognizerConfig{}
	cfg.FeatConfig.SampleRate = modelPaths.SampleRate
	cfg.FeatConfig.FeatureDim = modelPaths.FeatureDim
	cfg.ModelConfig.Transducer.Encoder = modelPaths.EncoderFile
	cfg.ModelConfig.Transducer.Decoder = modelPaths.DecoderFile
	cfg.ModelConfig.Transducer.Joiner = modelPaths.JoinerFile
	cfg.ModelConfig.Tokens = modelPaths.TokensFile
	cfg.ModelConfig.ModelType = modelPaths.ModelType
	cfg.ModelConfig.NumThreads = 8
	cfg.ModelConfig.Provider = "cpu"
	rec := sherpa.NewOfflineRecognizer(&cfg)
	if rec == nil {
		fmt.Fprintln(os.Stderr, "recognizer init failed")
		os.Exit(1)
	}
	defer sherpa.DeleteOfflineRecognizer(rec)

	vadCfg := sherpa.VadModelConfig{}
	vadCfg.SileroVad.Model = vadPath
	vadCfg.SileroVad.Threshold = 0.5
	vadCfg.SileroVad.MinSilenceDuration = 0.5
	vadCfg.SileroVad.MinSpeechDuration = 0.25
	vadCfg.SileroVad.WindowSize = 512
	vadCfg.SileroVad.MaxSpeechDuration = 25.0
	vadCfg.SampleRate = modelPaths.SampleRate
	vadCfg.NumThreads = 8
	vadCfg.Provider = "cpu"
	vadBufferSeconds := 60.0
	if v := os.Getenv("STTDEBUG_VAD_BUFFER"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			vadBufferSeconds = parsed
		}
	}
	fmt.Printf("vad buffer: %.0fs\n", vadBufferSeconds)
	vad := sherpa.NewVoiceActivityDetector(&vadCfg, float32(vadBufferSeconds))
	if vad == nil {
		fmt.Fprintln(os.Stderr, "vad init failed")
		os.Exit(1)
	}
	defer sherpa.DeleteVoiceActivityDetector(vad)

	chunk := 16000 * 5
	if os.Getenv("STTDEBUG_FEED_WINDOWS") != "" {
		chunk = 512 // feed exactly one VAD window per call, like the sherpa examples
	}
	fmt.Printf("feeding VAD in chunks of %d samples\n", chunk)
	for off := 0; off < len(samples); off += chunk {
		end := off + chunk
		if end > len(samples) {
			end = len(samples)
		}
		vad.AcceptWaveform(samples[off:end])
	}
	vad.Flush()

	segIdx := 0
	for !vad.IsEmpty() {
		seg := vad.Front()
		vad.Pop()
		if seg == nil || len(seg.Samples) == 0 {
			continue
		}
		segIdx++
		startS := float64(seg.Start) / float64(modelPaths.SampleRate)
		durS := float64(len(seg.Samples)) / float64(modelPaths.SampleRate)

		var sumSq float64
		peak := float32(0)
		for _, s := range seg.Samples {
			sumSq += float64(s) * float64(s)
			if s > peak {
				peak = s
			}
			if -s > peak {
				peak = -s
			}
		}
		rms := math.Sqrt(sumSq / float64(len(seg.Samples)))

		// Decode exactly like transcribeSegment does (including tail pad).
		chunkSamples := seg.Samples
		if len(chunkSamples) < 10*modelPaths.SampleRate {
			padded := make([]float32, len(chunkSamples)+modelPaths.SampleRate/2)
			copy(padded, chunkSamples)
			chunkSamples = padded
		}
		st := sherpa.NewOfflineStream(rec)
		st.AcceptWaveform(modelPaths.SampleRate, chunkSamples)
		rec.Decode(st)
		result := st.GetResult()
		nTokens := 0
		text := ""
		if result != nil {
			nTokens = len(result.Tokens)
			text = result.Text
		}
		sherpa.DeleteOfflineStream(st)

		fmt.Printf("seg %2d: start=%8.1fs dur=%5.1fs rms=%.4f peak=%.3f tokens=%-3d text=%q\n",
			segIdx, startS, durS, rms, peak, nTokens, text)
	}
	fmt.Printf("total VAD segments: %d\n", segIdx)

	// Manual decode windows, bypassing VAD entirely: STTDEBUG_WINDOWS="56.0-60.0,67.0-70.0"
	if wins := os.Getenv("STTDEBUG_WINDOWS"); wins != "" {
		for _, w := range strings.Split(wins, ",") {
			parts := strings.SplitN(w, "-", 2)
			if len(parts) != 2 {
				continue
			}
			a, _ := strconv.ParseFloat(parts[0], 64)
			b, _ := strconv.ParseFloat(parts[1], 64)
			i0 := int(a * float64(modelPaths.SampleRate))
			i1 := int(b * float64(modelPaths.SampleRate))
			if i1 > len(samples) {
				i1 = len(samples)
			}
			if i0 >= i1 {
				continue
			}
			chunkSamples := samples[i0:i1]
			if len(chunkSamples) < 10*modelPaths.SampleRate {
				padded := make([]float32, len(chunkSamples)+modelPaths.SampleRate/2)
				copy(padded, chunkSamples)
				chunkSamples = padded
			}
			var sumSq float64
			for _, s := range chunkSamples {
				sumSq += float64(s) * float64(s)
			}
			rms := math.Sqrt(sumSq / float64(len(chunkSamples)))
			st := sherpa.NewOfflineStream(rec)
			st.AcceptWaveform(modelPaths.SampleRate, chunkSamples)
			rec.Decode(st)
			result := st.GetResult()
			text := ""
			if result != nil {
				text = result.Text
			}
			sherpa.DeleteOfflineStream(st)
			fmt.Printf("manual %7.1f-%7.1fs rms=%.4f text=%q\n", a, b, rms, text)
		}
	}
}

// writeWav16kMono writes float32 samples as a 16 kHz mono s16 WAV file.
func writeWav16kMono(path string, samples []float32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataLen := len(samples) * 2
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	putLE32(hdr[4:8], uint32(36+dataLen))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	putLE32(hdr[16:20], 16)
	putLE16(hdr[20:22], 1) // PCM
	putLE16(hdr[22:24], 1) // mono
	putLE32(hdr[24:28], 16000)
	putLE32(hdr[28:32], 16000*2)
	putLE16(hdr[32:34], 2)
	putLE16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	putLE32(hdr[40:44], uint32(dataLen))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	buf := make([]byte, dataLen)
	for i, s := range samples {
		v := int16(s * 32767)
		buf[i*2] = byte(uint16(v))
		buf[i*2+1] = byte(uint16(v) >> 8)
	}
	_, err = f.Write(buf)
	return err
}

func putLE16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func putLE32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
