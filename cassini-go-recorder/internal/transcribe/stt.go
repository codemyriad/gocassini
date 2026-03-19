package transcribe

import (
	"fmt"
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

// vadChunkSamples is the number of samples fed to the VAD per call (5 seconds at 16 kHz).
const vadChunkSamples = 16000 * 5

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

// Transcribe runs VAD-segmented ASR on the given float32 samples (16 kHz, mono, [-1,1]).
// Silero VAD detects speech segments at natural silence boundaries; each segment is
// then passed to the ASR model. Word timestamps refer to the full recording timeline.
func (r *Recognizer) Transcribe(samples []float32, sampleRate int) ([]Word, error) {
	if len(samples) == 0 {
		return nil, nil
	}

	r.vad.Reset()

	// Feed audio to the VAD in 5-second chunks.
	for off := 0; off < len(samples); off += vadChunkSamples {
		end := off + vadChunkSamples
		if end > len(samples) {
			end = len(samples)
		}
		r.vad.AcceptWaveform(samples[off:end])
	}
	r.vad.Flush()

	var allWords []Word
	for !r.vad.IsEmpty() {
		seg := r.vad.Front()
		r.vad.Pop()
		if seg == nil || len(seg.Samples) == 0 {
			continue
		}

		// seg.Start is the sample index of the segment start within the full recording.
		segOffsetMS := int64(seg.Start) * 1000 / int64(sampleRate)

		words, err := r.transcribeSegment(seg.Samples, sampleRate, segOffsetMS)
		if err != nil {
			return nil, err
		}
		allWords = append(allWords, words...)
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
			words = append(words, Word{
				Text:    text,
				StartMS: int64(wordStartMs),
				EndMS:   int64(lastEndMs),
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
				if dur > 0 {
					// Word-start markers belong to the new word only; updating the
					// running end time here avoids smearing the previous word's end.
					lastEndMs = end
				}
			}
		} else {
			if wordStartMs < 0 {
				wordStartMs = ts
			}
			curText.WriteString(tok)
			if dur > 0 {
				lastEndMs = end
			}
		}
	}
	flush()
	return splitMultiWordTokens(words)
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
