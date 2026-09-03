package transcribe

import (
	"testing"

	"gocassini/internal/portable"
)

// The sanitizer's whole job is to hand the packer an id it will accept, so
// every result it produces has to pass the published grammar.
func TestSanitizeTranscriptIDProducesValidIDs(t *testing.T) {
	cases := map[string]string{
		"model_name":             "model-name",
		"Parakeet.TDT-0.6B-v3":   "parakeet-tdt-0-6b-v3",
		"canary_1b_flash":        "canary-1b-flash",
		"--leading-and-trailing": "leading-and-trailing",
	}
	for in, want := range cases {
		got := sanitizeTranscriptID(in)
		if got != want {
			t.Errorf("sanitizeTranscriptID(%q) = %q, want %q", in, got, want)
		}
		if got == "" {
			continue
		}
		if err := portable.ValidateTranscriptID(got); err != nil {
			t.Errorf("sanitizeTranscriptID(%q) = %q, which the packer rejects: %v", in, got, err)
		}
	}
}
