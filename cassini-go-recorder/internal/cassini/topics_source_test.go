package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gocassini/internal/topics"
)

// packTopicsFixture packs bundleDir and returns the .opus path and the
// payloadRef.sha256 of its default words transcript.
func packTopicsFixture(t *testing.T, bundleDir string) (string, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "01KWEKPZVEJWP9BYBPBX9ZRNDQ.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}
	manifest := decodePortableManifestFromOpus(t, out)
	for _, entry := range manifest.Transcripts {
		if entry.Default {
			return out, entry.PayloadRef.SHA256
		}
	}
	t.Fatalf("packed manifest has no default transcript: %+v", manifest.Transcripts)
	return "", ""
}

func checkTopicsSourcesAgree(t *testing.T, bundleDir string, wantWords []string) {
	t.Helper()
	opus, packed := packTopicsFixture(t, bundleDir)
	fromBundle, err := readTopicsTranscript(bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := readTopicsTranscript(opus)
	if err != nil {
		t.Fatal(err)
	}
	if fromBundle.PayloadSHA256 != packed || fromFile.PayloadSHA256 != packed {
		t.Fatalf("payload sha256: bundle %s, file %s, packed payloadRef %s", fromBundle.PayloadSHA256, fromFile.PayloadSHA256, packed)
	}
	if !slices.Equal(fromBundle.Words, fromFile.Words) {
		t.Fatalf("words differ:\n bundle %+v\n file   %+v", fromBundle.Words, fromFile.Words)
	}
	var texts []string
	for _, w := range fromFile.Words {
		texts = append(texts, w.Text)
	}
	if !slices.Equal(texts, wantWords) {
		t.Fatalf("words = %q, want %q", texts, wantWords)
	}
	if fromBundle.Language != fromFile.Language {
		t.Fatalf("language: bundle %q, file %q", fromBundle.Language, fromFile.Language)
	}
}

// The bundle the operator keeps and the .opus it delivered give the same
// transcript body, so the same embed key.
func TestTopicsSourceBundleAndOpusAgree(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := filepath.Join(t.TempDir(), "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatal(err)
	}
	checkTopicsSourcesAgree(t, bundleDir, []string{"hello", "team"})
	got, err := readTopicsTranscript(bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []topics.Word{{Text: "hello", StartMS: 0, EndMS: 80, Speaker: "spk_host"}, {Text: "team", StartMS: 100, EndMS: 200, Speaker: "spk_host"}}
	if !slices.Equal(got.Words, want) {
		t.Fatalf("words = %+v", got.Words)
	}
}

// With several transcripts (a separated-voices rebuild, or two engines) the
// default one is read, from either side.
func TestTopicsSourceReadsTheDefaultOfSeveralTranscripts(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := filepath.Join(t.TempDir(), "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(bundleDir, "transcript.words.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	other := strings.ReplaceAll(strings.ReplaceAll(string(original), `"hello"`, `"ciao"`), `"team"`, `"squadra"`)
	if err := os.WriteFile(filepath.Join(bundleDir, "transcript.other.words.v1.json"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["files"].(map[string]any)["transcripts"] = []map[string]any{
		{"id": "raw-asr", "path": "transcript.words.v1.json", "language": "en"},
		{"id": "other", "path": "transcript.other.words.v1.json", "default": true, "language": "it"},
	}
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	checkTopicsSourcesAgree(t, bundleDir, []string{"ciao", "squadra"})
	got, err := readTopicsTranscript(bundleDir)
	if err != nil || got.Language != "it" {
		t.Fatalf("language = %q, %v", got.Language, err)
	}
}
