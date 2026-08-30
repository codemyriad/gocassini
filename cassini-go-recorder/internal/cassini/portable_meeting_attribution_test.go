package cassini

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// TestPackedPortableMeetingCarriesAttributionEvidence packs a bundle whose
// transcript.words.v1.json carries one attribution-flagged word and one
// unmeasured word through the real `cassini pack` path, then reads the
// published .opus back and asserts the evidence survives the wire format:
// the flagged item carries attributionGapDb and lowConfidenceSpeaker, the
// unmeasured item carries neither key, and every key an item emits is
// declared by the spec schema (which pins additionalProperties: false).
func TestPackedPortableMeetingCarriesAttributionEvidence(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "attribution.meeting")
	if err := writeAttributionMeetingBundleFixture(bundleDir); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	outPath := filepath.Join(tmp, "attribution.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	// Wire level: decode the default words-transcript body chunk set exactly
	// as a consumer would and look at the JSON keys, not a Go struct, so an
	// omitempty regression cannot hide behind zero values.
	tags, err := portableMeetingTags(outPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	items := decodeDefaultTranscriptItemsForTest(t, tags)
	if len(items) != 2 {
		t.Fatalf("expected 2 transcript items, got %d", len(items))
	}

	flagged := items[0]
	if got, ok := flagged["attributionGapDb"].(float64); !ok || got != 21.5 {
		t.Errorf("flagged item attributionGapDb = %v, want 21.5", flagged["attributionGapDb"])
	}
	if got, ok := flagged["lowConfidenceSpeaker"].(bool); !ok || !got {
		t.Errorf("flagged item lowConfidenceSpeaker = %v, want true", flagged["lowConfidenceSpeaker"])
	}
	unmeasured := items[1]
	if _, ok := unmeasured["attributionGapDb"]; ok {
		t.Errorf("unmeasured item must not carry attributionGapDb, got %v", unmeasured["attributionGapDb"])
	}
	if _, ok := unmeasured["lowConfidenceSpeaker"]; ok {
		t.Errorf("unmeasured item must not carry lowConfidenceSpeaker, got %v", unmeasured["lowConfidenceSpeaker"])
	}

	// Schema conformance, in the style of
	// TestPackedMeetingKeysAreDeclaredBySchema: every key an item emits must
	// be declared by the v1 transcript item schema — the one place the spec
	// constrains item bodies; the v2/v3 index schemas reference the same
	// cassini.words.v1 body by payloadRef without re-declaring its shape.
	assertTranscriptItemKeysDeclaredBySchema(t, items)

	// Typed read path: the same evidence must come back out of the published
	// file through the extraction API the CLI uses.
	extracted, err := inspectpkg.ExtractTranscriptWords(outPath)
	if err != nil {
		t.Fatalf("ExtractTranscriptWords: %v", err)
	}
	if len(extracted.Words) != 2 {
		t.Fatalf("expected 2 extracted words, got %d", len(extracted.Words))
	}
	if extracted.Words[0].AttributionGapDB == nil || *extracted.Words[0].AttributionGapDB != 21.5 {
		t.Errorf("extracted flagged word AttributionGapDB = %v, want 21.5", extracted.Words[0].AttributionGapDB)
	}
	if !extracted.Words[0].LowConfidenceSpeaker {
		t.Error("extracted flagged word LowConfidenceSpeaker = false, want true")
	}
	if extracted.Words[1].AttributionGapDB != nil {
		t.Errorf("extracted unmeasured word AttributionGapDB = %v, want nil", extracted.Words[1].AttributionGapDB)
	}
	if extracted.Words[1].LowConfidenceSpeaker {
		t.Error("extracted unmeasured word LowConfidenceSpeaker = true, want false")
	}
}

// writeAttributionMeetingBundleFixture is writeReadyMeetingBundleFixture with
// one word carrying the speaker-attribution evidence and one without it.
func writeAttributionMeetingBundleFixture(meetingDir string) error {
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		return err
	}
	if output, err := exec.Command(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=660:sample_rate=48000:duration=0.25",
		"-c:a", "libopus",
		"-application", "voip",
		filepath.Join(meetingDir, "meeting.webm"),
	).CombinedOutput(); err != nil {
		return fmt.Errorf("write meeting audio: %w: %s", err, strings.TrimSpace(string(output)))
	}
	transcript := `{
  "version": "transcript.words.v1",
  "media": {"src": "meeting.webm", "durationMs": 250},
  "speakers": [{"id": "spk_host", "label": "Host"}],
  "segments": [
    {
      "speaker": "spk_host",
      "startMs": 0,
      "endMs": 200,
      "text": "hello team",
      "words": [
        {"text": "hello", "startMs": 0, "endMs": 80, "attributionGapDb": 21.5, "lowConfidenceSpeaker": true},
        {"text": "team", "startMs": 100, "endMs": 200}
      ]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript.words.v1.json"), []byte(transcript), 0o644); err != nil {
		return err
	}
	manifest := `{
  "version": "cassini.meeting-artifact.v1",
  "generatedAt": "2026-03-11T10:00:00Z",
  "source": {"basename": "source.mkv", "durationMs": 250},
  "files": {"audio": "meeting.webm", "transcript": "transcript.words.v1.json"},
  "speakerCount": 1,
  "wordCount": 2
}
`
	if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		return err
	}
	bundle := MeetingBundle{
		RootDir:      meetingDir,
		ManifestPath: filepath.Join(meetingDir, "cassini.json"),
	}
	return FinalizeMeetingBundle(bundle, MeetingBundleManifest{
		SourceKind: "mkv",
		SourcePath: "/tmp/source.mkv",
	})
}

// decodeDefaultTranscriptItemsForTest reverses the CASSINI_TX_<ID>_PAYLOAD_*
// chunk set of the file's default transcript into raw JSON items, keeping
// each item as a key/value map so tests can assert on key presence.
func decodeDefaultTranscriptItemsForTest(t *testing.T, tags map[string]string) []map[string]any {
	t.Helper()
	id := portableTagValue(tags, "CASSINI_TRANSCRIPT_DEFAULT")
	if id == "" {
		t.Fatal("packed file declares no CASSINI_TRANSCRIPT_DEFAULT")
	}
	prefix := portable.TranscriptIDToTagPrefix(id)
	chunkCount, err := strconv.Atoi(portableTagValue(tags, prefix+"CHUNK_COUNT"))
	if err != nil || chunkCount <= 0 {
		t.Fatalf("missing or invalid %sCHUNK_COUNT", prefix)
	}
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("%s%03d", prefix, idx)
		chunk := portableTagValue(tags, key)
		if chunk == "" {
			t.Fatalf("missing transcript chunk %s", key)
		}
		encoded.WriteString(chunk)
	}
	compressed, err := base64.RawURLEncoding.DecodeString(encoded.String())
	if err != nil {
		t.Fatalf("decode base64url transcript payload: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open gzip transcript payload: %v", err)
	}
	defer func() { _ = reader.Close() }()
	rawJSON, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress transcript payload: %v", err)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rawJSON, &body); err != nil {
		t.Fatalf("parse transcript body JSON: %v", err)
	}
	return body.Items
}

// assertTranscriptItemKeysDeclaredBySchema checks every emitted item key
// against the v1 schema's transcript item declaration, which the spec pins
// with additionalProperties: false.
func assertTranscriptItemKeysDeclaredBySchema(t *testing.T, items []map[string]any) {
	t.Helper()
	const schemaPath = "../../../spec/cassini-portable-meeting-manifest-v1.schema.json"
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", schemaPath, err)
	}
	itemSchema := nested(doc, "properties", "transcript", "properties", "items", "items")
	if itemSchema == nil {
		t.Fatalf("%s has no transcript item schema", schemaPath)
	}
	if additional, ok := itemSchema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("%s: transcript item additionalProperties is not false; this test assumes it is", schemaPath)
	}
	declared, _ := itemSchema["properties"].(map[string]any)
	for idx, item := range items {
		for key := range item {
			if _, ok := declared[key]; !ok {
				t.Errorf("%s does not declare transcript item key %q (item %d)", schemaPath, key, idx)
			}
		}
	}
}
