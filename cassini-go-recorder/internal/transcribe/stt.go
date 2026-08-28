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

// vadWindowSamples is the configured SileroVad.WindowSize. sherpa-onnx can
// buffer a non-window remainder between AcceptWaveform calls, but its Flush
// method does not evaluate that final remainder. Feeding exact windows (and
// zero-padding only the final one) therefore preserves every input sample and
// follows the upstream detector's intended call shape.
const vadWindowSamples = 512

// vadDrainEverySamples controls how often queued speech segments are popped
// and transcribed while feeding (every ~5 seconds at 16 kHz). Draining as we
// go keeps the VAD's internal circular buffer small instead of letting whole
// sparse tracks accumulate.
const vadDrainEverySamples = 16000 * 5

// maxSafeSegmentSamples is the safety-fallback split size (55 seconds at 16 kHz).
// VAD MaxSpeechDuration=25s keeps segments short, but pathological silence-free
// speech could still exceed that; 55s gives a comfortable ONNX-safe ceiling.
const maxSafeSegmentSamples = 16000 * 55

// nonVADWindowSamples / nonVADWindowOverlapSamples define the fixed sliding
// window used by the non-VAD (useVAD=false) chunked decode. The merged-mix
// fallback feeds ~75s of dense audio with no silence boundaries; decoding it
// as one or two huge spans makes the int8 Parakeet decoder land a single
// low-confidence result whose word run is unstable across runs. Splitting into
// short (~15s) windows keeps each int8 decode short and high-confidence, so one
// bad span can no longer zero the whole transcript. The 0.5s overlap ensures a
// word straddling a window boundary is captured by at least one window; the
// overlap is de-duplicated by word timestamp (see transcribeNonVADChunked).
const (
	nonVADWindowSamples        = 16000 * 15 // 15s window at 16 kHz
	nonVADWindowOverlapSamples = 16000 / 2  // 0.5s overlap at 16 kHz
)

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
	vadCfg.SileroVad.WindowSize = vadWindowSamples
	vadCfg.SileroVad.MaxSpeechDuration = 25.0
	vadCfg.SampleRate = paths.SampleRate
	// Silero VAD is a tiny stateful model run per 32 ms window; it is fastest
	// single-threaded on CPU. Running it on a GPU provider turns each window
	// into a micro kernel-launch (measured ~3x slower on sparse/long streams),
	// so VAD stays on CPU regardless of the recogniser device. See vadProvider.
	vadCfg.NumThreads = 1
	vadCfg.Provider = vadProvider()
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
// When useVAD is false, the input is split into fixed overlapping windows
// (~15s window, ~0.5s overlap) and each window is decoded through
// transcribeSegment, with words de-duplicated across the overlap region by
// timestamp (see transcribeNonVADChunked). Use this when the caller already
// knows the audio is dense and continuous — the merged-fallback path against
// the rotated mix is the canonical case. A single full-length decode of that
// ~75s mix lands one giant low-confidence int8 span whose verbatim word run is
// unstable run-to-run; short windows keep each int8 decode short and
// high-confidence so one bad span can't zero the whole transcript. Bypassing
// VAD also removes the Silero failure surface — its default 0.5 threshold has
// been observed to reject loud (-19 to -29 dB) dense audio in ~17-33% of CI
// runs.
//
// Word timestamps refer to the full recording timeline either way.
func (r *Recognizer) Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error) {
	if len(samples) == 0 {
		return nil, nil
	}

	if !useVAD {
		words, err := r.transcribeNonVADChunked(samples, sampleRate)
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

	// Feed audio to the VAD one configured window per call. Draining completed
	// segments periodically is the important memory bound: without it, a long
	// sparse track leaves completed speech queued while the internal circular
	// buffer grows for the duration of the recording.
	sinceDrain := 0
	for off := 0; off < len(samples); off += vadWindowSamples {
		end := off + vadWindowSamples
		if end > len(samples) {
			end = len(samples)
		}
		window := samples[off:end]
		if len(window) < vadWindowSamples {
			// Flush only closes an already-detected speech segment; it does not
			// run the detector over sherpa-onnx's buffered partial window.
			padded := make([]float32, vadWindowSamples)
			copy(padded, window)
			window = padded
		}
		r.vad.AcceptWaveform(window)
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

// transcribeNonVADChunked decodes dense, silence-free audio (the merged-mix
// fallback case) by sliding a fixed window over the input instead of handing
// the whole buffer to a single decode. Each window is transcribed via the same
// transcribeSegment / NewOfflineStream path the VAD segments use — so it still
// gets the per-segment decoder tail pad — and its words already carry
// full-recording timestamps because we pass the window start as segOffsetMS.
// Adjacent windows overlap by nonVADWindowOverlapSamples so a word straddling a
// boundary is captured by at least one window; the overlap is de-duplicated by
// timestamp in dedupOverlappingWords.
func (r *Recognizer) transcribeNonVADChunked(samples []float32, sampleRate int) ([]Word, error) {
	windowSamples := scaleSamples(nonVADWindowSamples, sampleRate)
	overlapSamples := scaleSamples(nonVADWindowOverlapSamples, sampleRate)

	overlapMS := int64(overlapSamples) * 1000 / int64(sampleRate)

	var allWords []Word
	firstWindow := true
	for _, win := range nonVADWindowBounds(len(samples), windowSamples, overlapSamples) {
		windowStartMS := int64(win.start) * 1000 / int64(sampleRate)
		words, err := r.transcribeSegment(samples[win.start:win.end], sampleRate, windowStartMS)
		if err != nil {
			return nil, err
		}
		// Drop words that fall inside the region already covered by the
		// previous window's tail (the overlap [windowStartMS, windowStartMS+
		// overlapMS]). dedupOverlappingWords cuts at the overlap midpoint, so a
		// word emitted by both windows is kept exactly once.
		allWords = dedupOverlappingWords(allWords, words, firstWindow, windowStartMS, overlapMS)
		firstWindow = false
	}
	return allWords, nil
}

// windowBound is a half-open [start,end) sample range for one decode window.
type windowBound struct {
	start int
	end   int
}

// nonVADWindowBounds returns the sliding-window sample ranges covering
// [0,total). Each window is windowSamples long (the last is shorter) and starts
// overlapSamples before the end of the previous window, i.e. the stride is
// windowSamples-overlapSamples. A non-positive or oversized window collapses to
// a single full-length window so the function never returns zero windows for a
// non-empty input and never loops forever.
func nonVADWindowBounds(total, windowSamples, overlapSamples int) []windowBound {
	if total <= 0 {
		return nil
	}
	if windowSamples <= 0 || windowSamples >= total {
		return []windowBound{{start: 0, end: total}}
	}
	stride := windowSamples - overlapSamples
	if stride <= 0 {
		stride = windowSamples
	}
	var bounds []windowBound
	for start := 0; start < total; start += stride {
		end := start + windowSamples
		if end >= total {
			bounds = append(bounds, windowBound{start: start, end: total})
			break
		}
		bounds = append(bounds, windowBound{start: start, end: end})
	}
	return bounds
}

// dedupOverlappingWords merges the next window's words into acc, de-duplicating
// the overlap region [windowStartMS, windowStartMS+overlapMS] that both windows
// decoded. The cut is the overlap midpoint: the earlier window (already in acc)
// owns words that start before the cut, the later window (next) owns words that
// start at or after it. We therefore trim acc's trailing words that start at/after
// the cut and keep next's words that start at/after the cut, so a word emitted by
// both windows survives exactly once and the seam falls in a low-traffic point of
// the overlap. firstWindow keeps the first window's words verbatim (no preceding
// overlap). Keeping the rule a pure timestamp comparison makes it deterministic
// and model-free for testing.
func dedupOverlappingWords(acc, next []Word, firstWindow bool, windowStartMS, overlapMS int64) []Word {
	if firstWindow {
		return append(acc, next...)
	}
	cutMS := windowStartMS + overlapMS/2
	// Trim acc's tail that reaches into the later window's half of the overlap.
	trimmed := len(acc)
	for trimmed > 0 && acc[trimmed-1].StartMS >= cutMS {
		trimmed--
	}
	acc = acc[:trimmed]
	// Append next's words from the cut onward (its share of the overlap plus the
	// rest of the window).
	for _, w := range next {
		if w.StartMS < cutMS {
			continue
		}
		acc = append(acc, w)
	}
	return acc
}

// scaleSamples rescales a sample count defined at 16 kHz to the given sample
// rate so the window/overlap durations stay constant if a model ever reports a
// different rate. At the canonical 16 kHz it is the identity.
func scaleSamples(samplesAt16k, sampleRate int) int {
	if sampleRate == 16000 || sampleRate <= 0 {
		return samplesAt16k
	}
	return samplesAt16k * sampleRate / 16000
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
