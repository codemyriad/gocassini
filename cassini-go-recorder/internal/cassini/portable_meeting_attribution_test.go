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

// TestPackedPortableMeetingCarriesAttributionRecord packs a drop-mode-shaped
// bundle — flagged words already deleted from the transcript, the artifact
// manifest's provenance.attribution as the only trace they existed — through
// the real `cassini pack` path, and asserts the record reaches the published
// wire verbatim, is declared by the closed v2/v3 schemas, comes back through
// the typed read path, and survives a retag. A sibling attribution-less pack
// must emit no attribution key at all.
func TestPackedPortableMeetingCarriesAttributionRecord(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()

	bundleDir := filepath.Join(tmp, "drop-mode.meeting")
	const attributionJSON = `{"ran": true, "mode": "drop", "wordsMeasured": 120, "wordsFlagged": 7, "wordsDropped": 7, "thresholdDb": 14.5}`
	if err := writeProvenancedMeetingBundleFixture(bundleDir, attributionJSON); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	outPath := filepath.Join(tmp, "drop-mode.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	wantAttribution := map[string]any{
		"ran":           true,
		"mode":          "drop",
		"wordsMeasured": float64(120),
		"wordsFlagged":  float64(7),
		"wordsDropped":  float64(7),
		"thresholdDb":   14.5,
	}
	assertPackedAttributionRecord(t, outPath, wantAttribution)

	// Typed read path: the same record must come back out of the published
	// file through the struct the CLI and exporter read.
	typed := decodePortableManifestFromOpus(t, outPath)
	if typed.Provenance == nil || typed.Provenance.Attribution == nil {
		t.Fatalf("typed manifest carries no provenance.attribution: %+v", typed.Provenance)
	}
	attribution := typed.Provenance.Attribution
	if !attribution.Ran || attribution.Mode != "drop" || attribution.Reason != "" {
		t.Errorf("typed attribution ran/mode/reason = %v/%q/%q, want true/drop/empty",
			attribution.Ran, attribution.Mode, attribution.Reason)
	}
	if attribution.WordsMeasured != 120 || attribution.WordsFlagged != 7 || attribution.WordsDropped != 7 {
		t.Errorf("typed attribution counts = %d/%d/%d, want 120/7/7",
			attribution.WordsMeasured, attribution.WordsFlagged, attribution.WordsDropped)
	}
	if attribution.ThresholdDB == nil || *attribution.ThresholdDB != 14.5 {
		t.Errorf("typed attribution thresholdDb = %v, want 14.5", attribution.ThresholdDB)
	}

	// Retag edits the payload as a generic JSON document; the record must ride
	// through an unrelated edit untouched.
	retaggedPath := filepath.Join(tmp, "drop-mode-retagged.opus")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{
		"retag", outPath, "--out", retaggedPath, "--room-id", "rm_9f2a1c3d4e5b6a70",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}
	assertPackedAttributionRecord(t, retaggedPath, wantAttribution)

	// An attribution-less build with other provenance must not grow the key.
	plainDir := filepath.Join(tmp, "no-attribution.meeting")
	if err := writeProvenancedMeetingBundleFixture(plainDir, ""); err != nil {
		t.Fatalf("write attribution-less fixture: %v", err)
	}
	plainPath := filepath.Join(tmp, "no-attribution.opus")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"pack", plainDir, "--out", plainPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack (attribution-less) failed code=%d stderr=%q", code, stderr.String())
	}
	plainProvenance := decodePackedProvenanceForTest(t, plainPath)
	if plainProvenance == nil {
		t.Fatal("attribution-less pack lost its provenance object entirely")
	}
	if _, ok := plainProvenance["attribution"]; ok {
		t.Errorf("attribution-less pack emits an attribution key: %v", plainProvenance["attribution"])
	}
	if _, ok := plainProvenance["speechToText"]; !ok {
		t.Error("attribution-less pack lost provenance.speechToText, so this half of the test is not exercising a provenance-carrying file")
	}
}

// assertPackedAttributionRecord reads the published file's wire manifest as
// raw JSON — key level, not a Go struct, so an omitempty regression cannot
// hide behind zero values — and asserts provenance.attribution matches `want`
// exactly and is fully declared by the closed v2/v3 schemas.
func assertPackedAttributionRecord(t *testing.T, path string, want map[string]any) {
	t.Helper()
	provenance := decodePackedProvenanceForTest(t, path)
	if provenance == nil {
		t.Fatalf("%s: packed manifest has no provenance object", path)
	}
	attribution, ok := provenance["attribution"].(map[string]any)
	if !ok {
		t.Fatalf("%s: packed provenance has no attribution object: %v", path, provenance)
	}
	for key, wanted := range want {
		if got, ok := attribution[key]; !ok || got != wanted {
			t.Errorf("%s: attribution[%q] = %v (present=%v), want %v", path, key, got, ok, wanted)
		}
	}
	for key := range attribution {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: attribution carries unexpected key %q = %v", path, key, attribution[key])
		}
	}
	assertAttributionKeysDeclaredBySchema(t, attribution)
}

// decodePackedProvenanceForTest returns the `provenance` object of the file's
// main wire manifest, or nil when the manifest has none.
func decodePackedProvenanceForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	tags, err := portableMeetingTags(path)
	if err != nil {
		t.Fatalf("read tags of %s: %v", path, err)
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatalf("decode payload of %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(rawJSON, &document); err != nil {
		t.Fatalf("parse manifest of %s: %v", path, err)
	}
	provenance, _ := document["provenance"].(map[string]any)
	return provenance
}

// assertAttributionKeysDeclaredBySchema checks the emitted attribution object
// against both multi-transcript schemas, which pin the record down with
// additionalProperties: false — a key the producer emits and the schema does
// not declare makes every packed file invalid.
func assertAttributionKeysDeclaredBySchema(t *testing.T, attribution map[string]any) {
	t.Helper()
	for _, schemaPath := range []string{
		"../../../spec/cassini-portable-meeting-manifest-v3.schema.json",
		"../../../spec/cassini-portable-meeting-manifest-v2.schema.json",
	} {
		raw, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatalf("read %s: %v", schemaPath, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", schemaPath, err)
		}
		ref := nested(doc, "properties", "provenance", "properties", "attribution")
		if ref == nil {
			t.Fatalf("%s does not declare provenance.attribution", schemaPath)
		}
		refTarget, _ := ref["$ref"].(string)
		if refTarget != "#/$defs/attributionProvenance" {
			t.Fatalf("%s: provenance.attribution is %v, want a $ref to #/$defs/attributionProvenance", schemaPath, ref)
		}
		def := nested(doc, "$defs", "attributionProvenance")
		if def == nil {
			t.Fatalf("%s has no $defs.attributionProvenance", schemaPath)
		}
		if additional, ok := def["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s: attributionProvenance.additionalProperties is not false; this test assumes it is", schemaPath)
		}
		declared, _ := def["properties"].(map[string]any)
		for key := range attribution {
			if _, ok := declared[key]; !ok {
				t.Errorf("%s does not declare attribution key %q", schemaPath, key)
			}
		}
		required, _ := def["required"].([]any)
		for _, name := range required {
			key, _ := name.(string)
			if _, ok := attribution[key]; !ok {
				t.Errorf("emitted attribution object lacks %q, which %s requires", key, schemaPath)
			}
		}
	}
}

// writeProvenancedMeetingBundleFixture is writeAttributionMeetingBundleFixture
// with a provenance block in the artifact manifest.json and a drop-mode-shaped
// transcript: no per-word evidence, because in drop mode the flagged words —
// and the evidence they carried — are already gone. attributionJSON is the
// provenance.attribution object, or "" for an attribution-less bundle.
func writeProvenancedMeetingBundleFixture(meetingDir string, attributionJSON string) error {
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
        {"text": "hello", "startMs": 0, "endMs": 80},
        {"text": "team", "startMs": 100, "endMs": 200}
      ]
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript.words.v1.json"), []byte(transcript), 0o644); err != nil {
		return err
	}
	attributionEntry := ""
	if attributionJSON != "" {
		attributionEntry = `,
    "attribution": ` + attributionJSON
	}
	manifest := `{
  "version": "cassini.meeting-artifact.v1",
  "generatedAt": "2026-03-11T10:00:00Z",
  "source": {"basename": "source.mkv", "durationMs": 250},
  "files": {"audio": "meeting.webm", "transcript": "transcript.words.v1.json"},
  "provenance": {
    "speechToText": {"backend": "sherpa-onnx-go", "model": "parakeet-tdt-0.6b-v3-int8", "device": "cpu"}` + attributionEntry + `
  },
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
