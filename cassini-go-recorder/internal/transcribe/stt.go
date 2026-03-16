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

// Recognizer wraps a sherpa-onnx offline recognizer.
type Recognizer struct {
	r *sherpa.OfflineRecognizer
}

// NewRecognizer creates an offline recognizer from the given model paths.
// provider is "cpu" or "cuda".
func NewRecognizer(paths ModelPaths, provider string, numThreads int) (*Recognizer, error) {
	if numThreads < 1 {
		numThreads = 4
	}
	cfg := sherpa.OfflineRecognizerConfig{}
	cfg.FeatConfig.SampleRate = paths.SampleRate
	cfg.FeatConfig.FeatureDim = paths.FeatureDim
	cfg.ModelConfig.NemoCTC.Model = paths.ModelFile
	cfg.ModelConfig.Tokens = paths.TokensFile
	cfg.ModelConfig.ModelType = paths.ModelType
	cfg.ModelConfig.NumThreads = numThreads
	cfg.ModelConfig.Provider = provider
	cfg.ModelConfig.Debug = 0

	r := sherpa.NewOfflineRecognizer(&cfg)
	if r == nil {
		return nil, fmt.Errorf("failed to create sherpa-onnx recognizer (check model paths and provider %q)", provider)
	}
	return &Recognizer{r: r}, nil
}

// Transcribe runs ASR on the given float32 samples (16 kHz, mono, [-1,1]).
// Returns a slice of Word with millisecond timestamps.
func (r *Recognizer) Transcribe(samples []float32, sampleRate int) ([]Word, error) {
	stream := sherpa.NewOfflineStream(r.r)
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(sampleRate, samples)
	r.r.Decode(stream)
	result := stream.GetResult()

	return tokensToWords(result.Tokens, result.Timestamps, result.Durations), nil
}

// Close frees the underlying recognizer.
func (r *Recognizer) Close() {
	sherpa.DeleteOfflineRecognizer(r.r)
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
		if end > lastEndMs || i == 0 {
			lastEndMs = end
		}

		// BPE word-start marker (▁) or plain space token signals word boundary.
		if strings.HasPrefix(tok, "▁") || tok == " " || tok == "<space>" {
			flush()
			clean := strings.TrimPrefix(tok, "▁")
			if clean != "" && clean != " " {
				if wordStartMs < 0 {
					wordStartMs = ts
				}
				curText.WriteString(clean)
			}
		} else {
			if wordStartMs < 0 {
				wordStartMs = ts
			}
			curText.WriteString(tok)
		}
		if dur > 0 {
			lastEndMs = end
		}
	}
	flush()
	return words
}
