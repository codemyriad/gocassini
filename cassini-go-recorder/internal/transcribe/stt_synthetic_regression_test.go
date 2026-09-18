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
	defer recognizer.Close()
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
