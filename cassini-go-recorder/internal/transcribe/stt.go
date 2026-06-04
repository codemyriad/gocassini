package transcribe

import (
	"fmt"
	"log"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Word is a single transcribed word with millisecond timestamps.
type Word struct {
	Text    string
	StartMS int64
	EndMS   int64
}

// Recognizer wraps a sherpa-onnx offline recognizer with Silero VAD segmentation.
type Recognizer struct {
	r          *sherpa.OfflineRecognizer
	vad        *sherpa.VoiceActivityDetector
	sampleRate int
}

// vadWindowSamples is the number of samples fed to the VAD per AcceptWaveform
// call. It MUST equal the configured SileroVad.WindowSize: sherpa-onnx's VAD
// mis-tracks its sample position when calls aren't window-aligned (each call's
// remainder samples desync the reported segment start/content from the real
// audio), which surfaced as VAD segments full of silence and "0 words from
// N s of audio" failures. See cmd/sttdebug for the reproduction harness.
const vadWindowSamples = 512

// vadDrainEverySamples controls how often queued speech segments are popped
// and transcribed while feeding (every ~5 seconds at 16 kHz). Draining as we
// go keeps the VAD's internal circular buffer small instead of letting whole
// sparse tracks accumulate ("circular-buffer.cc Push:107 Overflow!" log
// noise).
const vadDrainEverySamples = 16000 * 5

// maxSafeSegmentSamples is the safety-fallback split size (55 seconds at 16 kHz).
// VAD MaxSpeechDuration=25s keeps segments short, but pathological silence-free
// speech could still exceed that; 55s gives a comfortable ONNX-safe ceiling.
const maxSafeSegmentSamples = 16000 * 55

// NewRecognizer creates an offline recognizer from the given model paths and a
// Silero VAD model. provider is "cpu" or "cuda"; vadModelPath is the path to
// silero_vad.onnx.
func NewRecognizer(paths ModelPaths, vadModelPath, provider string, numThreads int) (*Recognizer, error) {
	if numThreads < 1 {
		numThreads = 4
	}

	cfg := sherpa.OfflineRecognizerConfig{}
	cfg.FeatConfig.SampleRate = paths.SampleRate
	cfg.FeatConfig.FeatureDim = paths.FeatureDim
	if paths.EncoderFile != "" {
		// Transducer model (encoder + decoder + joiner).
		cfg.ModelConfig.Transducer.Encoder = paths.EncoderFile
		cfg.ModelConfig.Transducer.Decoder = paths.DecoderFile
		cfg.ModelConfig.Transducer.Joiner = paths.JoinerFile
	} else {
		// CTC model (single model file).
		cfg.ModelConfig.NemoCTC.Model = paths.ModelFile
	}
	cfg.ModelConfig.Tokens = paths.TokensFile
	cfg.ModelConfig.ModelType = paths.ModelType
	cfg.ModelConfig.NumThreads = numThreads
	cfg.ModelConfig.Provider = provider
	cfg.ModelConfig.Debug = 0

	r := sherpa.NewOfflineRecognizer(&cfg)
	if r == nil {
		return nil, fmt.Errorf("failed to create sherpa-onnx recognizer (check model paths and provider %q)", provider)
	}

	vadCfg := sherpa.VadModelConfig{}
	vadCfg.SileroVad.Model = vadModelPath
	vadCfg.SileroVad.Threshold = 0.5
	vadCfg.SileroVad.MinSilenceDuration = 0.5
	vadCfg.SileroVad.MinSpeechDuration = 0.25
	vadCfg.SileroVad.WindowSize = 512
	vadCfg.SileroVad.MaxSpeechDuration = 25.0
	vadCfg.SampleRate = paths.SampleRate
	vadCfg.NumThreads = numThreads
	vadCfg.Provider = provider
	vadCfg.Debug = 0

	vad := sherpa.NewVoiceActivityDetector(&vadCfg, 60.0)
	if vad == nil {
		sherpa.DeleteOfflineRecognizer(r)
		return nil, fmt.Errorf("failed to create Silero VAD (check model path: %s)", vadModelPath)
	}

	return &Recognizer{r: r, vad: vad, sampleRate: paths.SampleRate}, nil
}

// Transcribe runs ASR on the given float32 samples (16 kHz, mono, [-1,1]).
//
// When useVAD is true, Silero VAD chunks the input at natural silence
// boundaries before each segment is sent to the ASR model. This is the
// right choice for genuinely sparse speech (per-participant tracks where
// each bot is muted ~25/30s per rotation, real meetings with pauses).
//
// When useVAD is false, the entire input is sent through transcribeSegment
// as a single span (which still applies the maxSafeSegmentSamples=55s split).
// Use this when the caller already knows the audio is dense and continuous
// — the merged-fallback path against the rotated mix is the canonical case.
// Silero with the default 0.5 threshold has been observed to reject loud
// (-19 to -29 dB) dense audio in ~17-33% of CI runs; bypassing VAD where it
// adds no value removes that failure surface.
//
// Word timestamps refer to the full recording timeline either way.
func (r *Recognizer) Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error) {
	if len(samples) == 0 {
		return nil, nil
	}

	if !useVAD {
		words, err := r.transcribeSegment(samples, sampleRate, 0)
		if err != nil {
			return nil, err
		}
		if len(words) == 0 && len(samples) >= sampleRate*5 {
			audioSeconds := float64(len(samples)) / float64(sampleRate)
			log.Printf("transcribe: 0 words from %.1fs of audio (VAD bypassed); ASR returned no tokens", audioSeconds)
		}
		return words, nil
	}

	r.vad.Reset()

	var allWords []Word
	var segCount int
	var totalSpeechSamples int

	// drainSegments transcribes every speech segment the VAD has queued so far.
	drainSegments := func() error {
		for !r.vad.IsEmpty() {
			seg := r.vad.Front()
			r.vad.Pop()
			if seg == nil || len(seg.Samples) == 0 {
				continue
			}
			segCount++
			totalSpeechSamples += len(seg.Samples)

			// seg.Start is the sample index of the segment start within the full recording.
			segOffsetMS := int64(seg.Start) * 1000 / int64(sampleRate)

			words, err := r.transcribeSegment(seg.Samples, sampleRate, segOffsetMS)
			if err != nil {
				return err
			}
			allWords = append(allWords, words...)
		}
		return nil
	}

	// Feed audio to the VAD strictly one window (512 samples) per call —
	// anything else desyncs sherpa-onnx's internal sample accounting (see
	// vadWindowSamples doc comment) and produces speech segments pointing at
	// silence. Queued segments are drained every ~5 s of fed audio so the
	// VAD's circular buffer stays small on long tracks.
	sinceDrain := 0
	for off := 0; off < len(samples); off += vadWindowSamples {
		end := off + vadWindowSamples
		if end > len(samples) {
			end = len(samples)
		}
		r.vad.AcceptWaveform(samples[off:end])
		sinceDrain += end - off
		if sinceDrain >= vadDrainEverySamples {
			sinceDrain = 0
			if err := drainSegments(); err != nil {
				return nil, err
			}
		}
	}
	r.vad.Flush()
	if err := drainSegments(); err != nil {
		return nil, err
	}
	// When a non-trivial audio buffer produces zero words, we want enough
	// information in the log to tell VAD-rejected-everything from
	// ASR-returned-no-tokens — the e2e harness keeps observing this
	// failure intermittently and the two cases have very different fixes.
	if len(allWords) == 0 && len(samples) >= sampleRate*5 {
		audioSeconds := float64(len(samples)) / float64(sampleRate)
		speechSeconds := float64(totalSpeechSamples) / float64(sampleRate)
		log.Printf("transcribe: 0 words from %.1fs of audio; VAD segments=%d totalling %.1fs of speech", audioSeconds, segCount, speechSeconds)
	}
	return allWords, nil
}

// transcribeSegment transcribes a single VAD speech segment, splitting into sub-chunks
// if the segment exceeds maxSafeSegmentSamples. segOffsetMS is the ms position of
// seg[0] within the full recording.
func (r *Recognizer) transcribeSegment(samples []float32, sampleRate int, segOffsetMS int64) ([]Word, error) {
	var allWords []Word
	for start := 0; start < len(samples); start += maxSafeSegmentSamples {
		end := start + maxSafeSegmentSamples
		if end > len(samples) {
			end = len(samples)
		}
		chunk := samples[start:end]

		// Parakeet TDT v3 returns zero tokens on speech chunks of ~6–9s
		// that have no trailing silence — the decoder treats the
		// abrupt end as mid-utterance and emits nothing. Pad short
		// chunks (<10s) with 0.5s of silence so the decoder sees an
		// utterance boundary. Longer chunks already include trailing
		// silence and are unaffected; padding longer than ~0.5s starts
		// trimming the LibriSpeech reference, so we keep the tail
		// short.
		const decoderTailPadMinSeconds = 10
		if len(chunk) < decoderTailPadMinSeconds*sampleRate {
			padded := make([]float32, len(chunk)+sampleRate/2) // +0.5s
			copy(padded, chunk)
			chunk = padded
		}

		stream := sherpa.NewOfflineStream(r.r)
		if stream == nil {
			return nil, fmt.Errorf("failed to create offline stream")
		}
		stream.AcceptWaveform(sampleRate, chunk)
		r.r.Decode(stream)
		result := stream.GetResult()
		var words []Word
		if result != nil {
			// Copy result data before deleting the stream — result may point
			// into stream-owned memory.
			words = tokensToWords(result.Tokens, result.Timestamps, result.Durations)
		}
		sherpa.DeleteOfflineStream(stream)

		// Offset timestamps: chunk start within segment + segment start within recording.
		chunkOffsetMS := segOffsetMS + int64(start)*1000/int64(sampleRate)
		for i := range words {
			words[i].StartMS += chunkOffsetMS
			words[i].EndMS += chunkOffsetMS
		}
		allWords = append(allWords, words...)
	}
	return allWords, nil
}

// Close frees the underlying recognizer and VAD.
func (r *Recognizer) Close() {
	sherpa.DeleteOfflineRecognizer(r.r)
	sherpa.DeleteVoiceActivityDetector(r.vad)
}

// tokensToWords converts BPE/char tokens with per-token timestamps into
// word-level timings. Handles both BPE (▁ word-start marker) and
// character-level CTC (space as delimiter).
func tokensToWords(tokens []string, timestamps, durations []float32) []Word {
	if len(tokens) == 0 {
		return nil
	}

	var words []Word
	var curText strings.Builder
	var wordStartMs float64 = -1
	var lastEndMs float64

	flush := func() {
		text := strings.TrimSpace(curText.String())
		if text != "" {
			endMs := lastEndMs
			if endMs < wordStartMs {
				endMs = wordStartMs
			}
			words = append(words, Word{
				Text:    text,
				StartMS: int64(wordStartMs),
				EndMS:   int64(endMs),
			})
		}
		curText.Reset()
		wordStartMs = -1
	}

	for i, tok := range tokens {
		var ts, dur float64
		if i < len(timestamps) {
			ts = float64(timestamps[i]) * 1000
		}
		if i < len(durations) {
			dur = float64(durations[i]) * 1000
		}
		end := ts + dur

		// Sherpa marks word starts in a few different ways depending on the
		// model: SentencePiece-style ▁ prefixes, standalone space tokens, and
		// literal leading spaces like " So". Missing the last form collapses an
		// entire phrase into one long token, which destroys the original pause
		// structure and leads to visibly wrong seek points in the UI.
		if strings.HasPrefix(tok, "▁") || strings.HasPrefix(tok, " ") || tok == "<space>" {
			flush()
			clean := strings.TrimPrefix(tok, "▁")
			clean = strings.TrimLeft(clean, " ")
			if clean != "" {
				if wordStartMs < 0 {
					wordStartMs = ts
				}
				curText.WriteString(clean)
				// Some models emit zero-duration boundary tokens. Keep the running
				// word end at least at the latest token timestamp so we never flush
				// a new word with the previous word's end time.
				lastEndMs = maxFloat64(lastEndMs, ts, end)
			}
		} else {
			if wordStartMs < 0 {
				wordStartMs = ts
			}
			curText.WriteString(tok)
			lastEndMs = maxFloat64(lastEndMs, ts, end)
		}
	}
	flush()
	return splitMultiWordTokens(words)
}

func maxFloat64(values ...float64) float64 {
	max := values[0]
	for _, value := range values[1:] {
		if value > max {
			max = value
		}
	}
	return max
}

func splitMultiWordTokens(words []Word) []Word {
	out := make([]Word, 0, len(words))
	for _, word := range words {
		parts := strings.Fields(word.Text)
		if len(parts) <= 1 {
			out = append(out, word)
			continue
		}

		// Some sherpa models now emit phrase-sized "tokens" with one shared time
		// span. Split those into real word entries here so downstream display
		// alignment and seek can anchor cleaned text to individual source words.
		span := word.EndMS - word.StartMS
		if span <= 0 {
			span = int64(len(parts))
		}
		for index, part := range parts {
			start := word.StartMS + (span*int64(index))/int64(len(parts))
			end := word.StartMS + (span*int64(index+1))/int64(len(parts))
			if end < start {
				end = start
			}
			out = append(out, Word{
				Text:    part,
				StartMS: start,
				EndMS:   end,
			})
		}
	}
	return out
}
