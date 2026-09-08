package portable

import (
	"encoding/json"
	"testing"
)

func TestWordOriginMetadataIsIgnored(t *testing.T) {
	encoded := encodePublishedFixture(t, publishedManifestFixture(Meeting{Title: "Origins"}))
	for _, origin := range []any{nil, "raw-asr", "scripted", "human-corrected", "translation", "unknown-origin", "display", 42, map[string]any{"future": true}} {
		label, _ := json.Marshal(origin)
		t.Run(string(label), func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(encoded.Main.JSON, &doc); err != nil {
				t.Fatal(err)
			}
			entry := doc["transcripts"].([]any)[0].(map[string]any)
			if origin != nil {
				entry["role"] = origin
				entry["sourceTranscriptId"] = map[string]any{"not": "a transcript id"}
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := DecodePublishedManifest(raw)
			if err != nil {
				t.Fatalf("decode words with ignored origin metadata: %v", err)
			}
			got := manifest.Transcripts[0]
			if got.ID != DefaultWordsTranscriptID || got.Role != "" || got.SourceTranscriptID != "" {
				t.Fatalf("word entry retained origin metadata: %+v", got)
			}
			if got.PayloadRef.SHA256 != encoded.Transcripts[0].Payload.SHA256 {
				t.Fatal("ignoring origin metadata changed the payload reference")
			}
		})
	}
}

func TestProducerOmitsWordOriginsAndKeepsDisplaySource(t *testing.T) {
	encoded, err := EncodePublishedManifest(publishedManifestFixture(Meeting{Title: "Words and display"}), []TranscriptInput{
		{ID: "words", Default: true, Body: sampleBody("spk_1", "hello")},
		{ID: "display", Role: RoleDisplay, SourceTranscriptID: "words", Default: true,
			Body: map[string]any{"version": "transcript.display.v1", "blocks": []any{}}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded.Main.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	words := doc["transcripts"].([]any)[0].(map[string]any)
	for _, key := range []string{"role", "sourceTranscriptId"} {
		if _, exists := words[key]; exists {
			t.Fatalf("new word transcript contains %s", key)
		}
	}
	display := doc["readableTranscripts"].([]any)[0].(map[string]any)
	if display["role"] != RoleDisplay || display["sourceTranscriptId"] != "words" {
		t.Fatalf("display lost its source: %+v", display)
	}
	if _, err := DecodePublishedManifest(encoded.Main.JSON); err != nil {
		t.Fatalf("decode newly produced file: %v", err)
	}
}
