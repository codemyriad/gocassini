package portable

import (
	"strings"
	"testing"
)

// The role table says which transcripts are derived from another and which are
// not: raw-asr came from the audio, scripted is what the audio performs, and
// the rest name their source.
func TestTranscriptRoleSourceRules(t *testing.T) {
	base := Manifest{
		Meeting:  Meeting{ID: "m", Title: "t", CreatedAtUTC: "2026-09-02T00:00:00Z"},
		Speakers: []Speaker{{ID: "spk1", Label: "Silvio"}},
	}
	body := TranscriptBody{Format: "cassini.words.v1", WordCount: 1,
		Items: []TranscriptItem{{Speaker: "spk1", StartMS: 0, EndMS: 10, Text: "hi"}}}

	cases := []struct {
		name    string
		input   TranscriptInput
		wantErr bool
	}{
		{"scripted without a source", TranscriptInput{ID: "script", Role: RoleScripted, Default: true, Body: body}, false},
		{"scripted with a source", TranscriptInput{ID: "script", Role: RoleScripted, Default: true, SourceTranscriptID: "raw-asr", Body: body}, true},
		{"raw-asr with a source", TranscriptInput{ID: "raw-asr", Role: RoleRawASR, Default: true, SourceTranscriptID: "other", Body: body}, true},
		{"human-corrected without a source", TranscriptInput{ID: "fixed", Role: RoleHumanCorrected, Default: true, Body: body}, true},
		{"translation without a source", TranscriptInput{ID: "italian", Role: RoleTranslation, Default: true, Body: body}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := NormalizePublishedManifest(base)
			manifest.Integrity.OpusSHA256 = strings.Repeat("a", 64)
			_, err := EncodePublishedManifest(manifest, []TranscriptInput{tc.input}, 4096)
			if tc.wantErr && err == nil {
				t.Errorf("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// A scripted transcript is words, so it fills the words slot and takes the
// words media type.
func TestScriptedTranscriptIsAWordsTranscript(t *testing.T) {
	encoded, err := EncodePublishedManifest(NormalizePublishedManifest(Manifest{
		Meeting:   Meeting{ID: "m", Title: "t", CreatedAtUTC: "2026-09-02T00:00:00Z"},
		Integrity: Integrity{OpusSHA256: strings.Repeat("a", 64)},
		Speakers:  []Speaker{{ID: "spk1", Label: "Silvio"}},
	}), []TranscriptInput{{
		ID: "script", Role: RoleScripted, Default: true,
		Body: TranscriptBody{Format: "cassini.words.v1", WordCount: 1,
			Items: []TranscriptItem{{Speaker: "spk1", StartMS: 0, EndMS: 10, Text: "hi"}}},
	}}, 4096)
	if err != nil {
		t.Fatalf("encode a scripted transcript: %v", err)
	}
	if len(encoded.Transcripts) != 1 {
		t.Fatalf("Transcripts = %d, want the scripted entry in the words array", len(encoded.Transcripts))
	}
	if got := encoded.Transcripts[0].Prefix; got != "CASSINI_TX_SCRIPT_PAYLOAD_" {
		t.Errorf("Prefix = %q", got)
	}
}
