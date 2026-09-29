package portable

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func retentionOpusFixture(t *testing.T) []byte {
	t.Helper()
	makeFile := func(tags []byte) []byte {
		segments := []byte{}
		left := len(tags)
		for left >= 255 {
			segments = append(segments, 255)
			left -= 255
		}
		segments = append(segments, byte(left))
		return joinOggPages(testOggPage(2, 11, 0, 0, testOpusHead(1, 312)), testOggPageWithSegments(0, 11, 1, 0, segments, tags), testOggPage(4, 11, 2, 960, []byte{0xf8, 0xff, 0xfe}))
	}
	integrity, err := ComputeOpusAudioIntegrity(bytes.NewReader(makeFile(testOpusTags("fixture"))))
	if err != nil {
		t.Fatal(err)
	}
	manifest := basePublishedManifest()
	manifest.Integrity.OpusSHA256 = integrity.SHA256
	manifest.Attachments = []map[string]any{{"name": "summary.md", "mime": "text/markdown", "contentBase64": "IyBTdW1tYXJ5"}}
	encoded, err := EncodePublishedManifest(manifest, []TranscriptInput{
		{ID: "english", Default: true, Format: "cassini.words.v1", Language: "en", WordCount: 2, Body: sampleBody("spk_0", "Hello", "world")},
		{ID: "spanish", Format: "cassini.words.v1", Language: "es", WordCount: 2, Body: sampleBody("spk_0", "Hola", "mundo")},
	}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(encoded.Main.JSON, &raw)
	raw["future"] = json.RawMessage(`{"history":[{"unknown":"preserve me"}]}`)
	main, _ := json.Marshal(raw)
	encoded.Main, err = EncodePayloadBytes(main, 4096)
	if err != nil {
		t.Fatal(err)
	}
	tags := BuildPublishedOpusTags(manifest, encoded, "english")
	keys := []string{}
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	comments := []string{"COMMENT=first", "COMMENT=second", "X-UNKNOWN=value"}
	for _, key := range keys {
		comments = append(comments, key+"="+tags[key])
	}
	packet := bytes.NewBufferString("OpusTags")
	binary.Write(packet, binary.LittleEndian, uint32(len("fixture")))
	packet.WriteString("fixture")
	binary.Write(packet, binary.LittleEndian, uint32(len(comments)))
	for _, comment := range comments {
		binary.Write(packet, binary.LittleEndian, uint32(len(comment)))
		packet.WriteString(comment)
	}
	return makeFile(packet.Bytes())
}

func TestTranscriptionPreservation(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	raw, report, err := ExportTranscription(bytes.NewReader(retentionOpusFixture(t)), TranscriptionOptions{DocumentID: "fixture-document", AgeAnchor: now.AddDate(0, 0, -90), AnchorSource: "recording-completed", EvictedAt: now, PolicyRevision: 7, Checkpoint: AnnotationCheckpoint{StateToken: "fixture:12", Revision: 0}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ReadTranscription(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Payloads) != 2 || len(report.Preserved) != 3 || len(report.ExcludedAudio) != 1 {
		t.Fatalf("incomplete inventory: %+v", report)
	}
	if !bytes.Contains(raw, []byte(`COMMENT=first`)) || !bytes.Contains(raw, []byte(`COMMENT=second`)) || !bytes.Contains(doc.ManifestRaw, []byte(`preserve me`)) {
		t.Fatal("lost metadata")
	}
	for _, entry := range report.Preserved {
		if entry.SHA256 == "" || entry.Bytes == 0 {
			t.Fatal("missing inventory evidence")
		}
	}
	if strings.Contains(string(raw), "OpusHead") || strings.Contains(string(raw), "OggS") {
		t.Fatal("audio container leaked")
	}
	// This shared fixture is explicitly regenerated, never rewritten in normal tests.
	if os.Getenv("UPDATE_TRANSCRIPTION_FIXTURE") == "1" {
		target := filepath.Join("..", "..", "..", "spec", "fixtures", "retained-meeting.json")
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var envelope map[string]any
	json.Unmarshal(raw, &envelope)
	envelope["futureEnvelope"] = map[string]any{"keep": true}
	withUnknown, _ := json.Marshal(envelope)
	annotations := json.RawMessage(`{"format":"cassini.annotations.v1","revision":1,"tagNamespace":"fixture","tags":[],"items":[]}`)
	rewritten, err := RewriteTranscriptionAnnotations(withUnknown, annotations, AnnotationCheckpoint{StateToken: "fixture:13", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := ReadTranscription(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rewritten, []byte(`futureEnvelope`)) || !bytes.Contains(updated.ManifestRaw, []byte(`preserve me`)) || updated.Checkpoint.StateToken != "fixture:13" {
		t.Fatal("rewrite lost unknown data or checkpoint")
	}
	for key, p := range doc.Payloads {
		if updated.Payloads[key] != p {
			t.Fatal("rewrite changed payload")
		}
	}
}

func TestTranscriptionRejectsInvalidJSON(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70), `{} {}`, `{"format":"something else"}`} {
		if _, err := ReadTranscription([]byte(raw)); err == nil {
			t.Fatal("accepted invalid document")
		}
	}
}
