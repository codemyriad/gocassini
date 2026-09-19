//go:build asrregression

package transcribe

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// TestSyntheticMeetingRetainsInteriorSpeech exercises the real extractor, VAD,
// frontend and decoder. The fixture contains only invented, synthesized speech.
// The pre-PR pipeline loses an interior phrase from the first track.
// This is an audio regression, not an assertion about which policy is selected.
func TestSyntheticMeetingRetainsInteriorSpeech(t *testing.T) {
	recognizer := newSyntheticRegressionRecognizer(t)
	fixture := filepath.Join("testdata", "synthetic-boundary", "garden.mkv")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != syntheticGardenSHA256 {
		t.Fatalf("synthetic fixture changed: sha256=%s", got)
	}
	streams, _, err := ProbeMKV(fixture)
	if err != nil || len(streams) != 2 {
		t.Fatalf("expected two synthetic participant tracks, got %d: %v", len(streams), err)
	}
	checks := [][]string{
		{
			"water the garden on tuesday",
			"moved the tools back into the shed and left the small pots under the porch",
			"if the weather clears tomorrow",
			"carry the empty baskets down to the gate",
		},
		{"children will arrive just after two", "pencils in the middle"},
	}
	for i, stream := range streams {
		t.Run(fmt.Sprintf("participant-%d", i+1), func(t *testing.T) {
			samples, err := ExtractSpeakerFloats(fixture, stream)
			if err != nil {
				t.Fatal(err)
			}
			words, err := recognizer.Transcribe(samples, 16000, true)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, word := range words {
				text.WriteString(word.Text)
				text.WriteByte(' ')
			}
			normalized := strings.ToLower(strings.Join(strings.FieldsFunc(text.String(), func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			}), " "))
			for _, phrase := range checks[i] {
				if !strings.Contains(" "+normalized+" ", " "+phrase+" ") {
					t.Errorf("missing synthetic speech %q; transcript: %s", phrase, text.String())
				}
			}
		})
	}
}

const syntheticGardenSHA256 = "12e309abe0d1d29f0248ebba88b8f2639ff1b870f907e1b9b856e7df6127e6b6"

func newSyntheticRegressionRecognizer(t *testing.T) *Recognizer {
	t.Helper()
	cache := os.Getenv("CASSINI_CACHE_ROOT")
	if cache == "" {
		t.Fatal("asrregression requires CASSINI_CACHE_ROOT with cached Parakeet v3 and Silero models")
	}
	provider := os.Getenv("CASSINI_ASR_REGRESSION_DEVICE")
	if provider == "" {
		provider = "cpu"
	}
	if provider != "cpu" && provider != "cuda" {
		t.Fatalf("unsupported ASR regression device %q", provider)
	}
	// Never make an explicit regression run silently download gigabytes or
	// skip the recognition assertion because a model is missing.
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")
	t.Setenv(envHintsDisabled, "")
	paths, err := EnsureModel(cache, ModelParakeet06BV3, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	vad, err := EnsureVAD(cache, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	decoder, _, err := resolveDecoder(t.TempDir(), nil, paths)
	if err != nil {
		t.Fatal(err)
	}
	recognizer, err := NewRecognizer(paths, vad, provider, 2, decoder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(recognizer.Close)
	return recognizer
}

// The tight VAD crop emitted no words on c580c394 even though decoding the
// complete waveform recognized the acknowledgement. Test recording boundaries
// and neighbouring speech as well as the original counterexample.
func TestSyntheticAcknowledgementRetainsSpeech(t *testing.T) {
	recognizer := newSyntheticRegressionRecognizer(t)
	fixture := filepath.Join("testdata", "synthetic-boundary", "acknowledgement.wav")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "169654b8fadb4255c8f835a6f89e1df348b6c93162aa2051fde7a54a510b23cb" {
		t.Fatalf("synthetic acknowledgement changed: sha256=%s", got)
	}
	samples, err := ExtractMixedFloats(fixture)
	if err != nil {
		t.Fatal(err)
	}
	withSilence := func(head, tail int) []float32 {
		out := make([]float32, head+len(samples)+tail)
		copy(out[head:], samples)
		return out
	}
	repeated := append(withSilence(0, 16000), samples...)
	for _, tc := range []struct {
		name    string
		samples []float32
		want    int
	}{
		{"recording-edges", samples, 1},
		{"recording-end", withSilence(16000, 0), 1},
		{"recording-start", withSilence(0, 16000), 1},
		{"interior", withSilence(16000, 16000), 1},
		{"two-turns", repeated, 2},
		{"silence", make([]float32, 32000), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words, err := recognizer.Transcribe(tc.samples, 16000, true)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, word := range words {
				text.WriteString(word.Text)
				text.WriteByte(' ')
				if word.StartMS < 0 || word.EndMS < word.StartMS || word.EndMS > int64(len(tc.samples))*1000/16000 {
					t.Errorf("word outside recording timeline: %+v", word)
				}
			}
			normalized := strings.ToLower(strings.Join(strings.FieldsFunc(text.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " "))
			if got := strings.Count(normalized, "right that makes sense"); got != tc.want {
				t.Fatalf("acknowledgement count=%d, want %d; transcript: %s", got, tc.want, text.String())
			}
			if tc.want == 0 && len(words) != 0 {
				t.Fatalf("hallucinated speech on silence: %s", text.String())
			}
		})
	}
}
