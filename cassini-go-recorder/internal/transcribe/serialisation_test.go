package transcribe

// Serialisation tests for the attribution evidence: what the written JSON
// artifacts actually carry, checked against the viewer's schema contract.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The written transcript.words.v1.json is the contract the viewer and the
// portable packer read. A measured+flagged word must carry attributionGapDb
// (as a number) and lowConfidenceSpeaker (true); an unmeasured word must
// carry neither key, so existing consumers see an unchanged document.
func TestTranscriptJSONSerialisesAttributionEvidence(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.words.v1.json")
	streams := []AudioStream{{SpeakerID: "spk_one", SpeakerLabel: "One"}}
	segments := []Segment{{SpeakerID: "spk_one", StartMS: 0, EndMS: 900, Text: "ghost clean",
		Words: []Word{
			{Text: "ghost", StartMS: 0, EndMS: 400,
				AttributionGapDB: 18.5, HasAttributionGap: true, LowConfidenceSpeaker: true},
			{Text: "clean", StartMS: 450, EndMS: 900},
		}}}

	if err := WriteTranscriptJSON(path, streams, segments, 900); err != nil {
		t.Fatalf("WriteTranscriptJSON: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var doc struct {
		Segments []struct {
			Words []map[string]any `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	if len(doc.Segments) != 1 || len(doc.Segments[0].Words) != 2 {
		t.Fatalf("expected one segment with two words, got %s", raw)
	}

	flagged := doc.Segments[0].Words[0]
	gap, ok := flagged["attributionGapDb"]
	if !ok {
		t.Fatal("the measured word lost attributionGapDb in the written JSON")
	}
	if num, isNum := gap.(float64); !isNum || num != 18.5 {
		t.Errorf("attributionGapDb must be the measured number, got %#v", gap)
	}
	if flagged["lowConfidenceSpeaker"] != true {
		t.Errorf("lowConfidenceSpeaker must serialise as true, got %#v", flagged["lowConfidenceSpeaker"])
	}

	unmeasured := doc.Segments[0].Words[1]
	if _, present := unmeasured["attributionGapDb"]; present {
		t.Error("an unmeasured word must not carry attributionGapDb")
	}
	if _, present := unmeasured["lowConfidenceSpeaker"]; present {
		t.Error("an unflagged word must not carry lowConfidenceSpeaker")
	}

	for _, w := range doc.Segments[0].Words {
		assertWordConformsToViewerSchema(t, w)
	}
}

// assertWordConformsToViewerSchema checks one emitted word object against the
// viewer's transcript-words-v1 schema. The package has no JSON Schema
// validator, so this is the structural core of one: the schema declares word
// with additionalProperties=false, so every emitted key must be declared, and
// the two attribution fields must be declared with types that admit what the
// producer writes.
func assertWordConformsToViewerSchema(t *testing.T, word map[string]any) {
	t.Helper()
	schemaPath := filepath.Join("..", "..", "..", "cassini-viewer", "schema", "transcript-words-v1.schema.json")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("viewer schema missing: %v", err)
	}
	var schema struct {
		Defs struct {
			Word struct {
				AdditionalProperties *bool                      `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
			} `json:"word"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse viewer schema: %v", err)
	}
	props := schema.Defs.Word.Properties
	if len(props) == 0 {
		t.Fatal("the viewer schema declares no word properties")
	}
	if schema.Defs.Word.AdditionalProperties == nil || *schema.Defs.Word.AdditionalProperties {
		t.Fatal("this check assumes word.additionalProperties=false in the viewer schema")
	}
	for _, field := range []struct{ name, typ string }{
		{"attributionGapDb", "number"},
		{"lowConfidenceSpeaker", "boolean"},
	} {
		declared, ok := props[field.name]
		if !ok {
			t.Errorf("viewer schema does not declare %s", field.name)
			continue
		}
		if !schemaTypeAdmits(t, declared, field.typ) {
			t.Errorf("viewer schema %s does not admit type %s: %s", field.name, field.typ, declared)
		}
	}
	for key := range word {
		if _, ok := props[key]; !ok {
			t.Errorf("emitted word key %q is not declared by the viewer schema; additionalProperties=false would reject the whole file", key)
		}
	}
}

// schemaTypeAdmits reports whether a schema property's "type" (a string or an
// array of strings, e.g. ["number", "null"]) includes want.
func schemaTypeAdmits(t *testing.T, declared json.RawMessage, want string) bool {
	t.Helper()
	var prop struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(declared, &prop); err != nil {
		t.Fatalf("parse schema property %s: %v", declared, err)
	}
	switch v := prop.Type.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if item == want {
				return true
			}
		}
	}
	return false
}

// Word.extentCap is decode-pipeline scaffolding — the ceiling the energy gate
// may extend a word's end up to — and it must never become transcript data.
// The written word object is pinned to its documented key set so a future
// field cannot slip into the contract the viewer and the portable packer read.
func TestTranscriptJSONNeverCarriesTheWordExtentCap(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.words.v1.json")
	streams := []AudioStream{{SpeakerID: "spk_one", SpeakerLabel: "One"}}
	segments := []Segment{{SpeakerID: "spk_one", StartMS: 0, EndMS: 900, Text: "capped word",
		Words: []Word{
			{Text: "capped", StartMS: 0, EndMS: 400, extentCap: 3600},
			{Text: "word", StartMS: 450, EndMS: 900, extentCap: 7777},
		}}}

	if err := WriteTranscriptJSON(path, streams, segments, 900); err != nil {
		t.Fatalf("WriteTranscriptJSON: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(raw), "3600") || strings.Contains(string(raw), "7777") {
		t.Fatalf("a word ceiling reached the written transcript: %s", raw)
	}
	var doc struct {
		Segments []struct {
			Words []map[string]any `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	if len(doc.Segments) != 1 || len(doc.Segments[0].Words) != 2 {
		t.Fatalf("expected one segment with two words, got %s", raw)
	}
	allowed := map[string]bool{
		"id": true, "text": true, "startMs": true, "endMs": true,
		"attributionGapDb": true, "lowConfidenceSpeaker": true,
	}
	for _, word := range doc.Segments[0].Words {
		for key := range word {
			if !allowed[key] {
				t.Errorf("unexpected key %q in a transcript word: %v", key, word)
			}
		}
	}
}
