package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	inspectpkg "gocassini/internal/inspect"
)

// sourceTranscriptWords reads the ordered word texts out of a build bundle's
// transcript.words.v1.json (segments[].words[].text), matching the phase-9
// roundtrip check's notion of "the words".
func sourceTranscriptWords(t *testing.T, transcriptPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read source transcript: %v", err)
	}
	var doc struct {
		Segments []struct {
			Words []struct {
				Text string `json:"text"`
			} `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse source transcript: %v", err)
	}
	words := []string{}
	for _, seg := range doc.Segments {
		for _, w := range seg.Words {
			if w.Text != "" {
				words = append(words, w.Text)
			}
		}
	}
	return words
}

// TestExtractTranscriptWordsFromPackedOpus packs a ready .meeting bundle to a
// portable .opus (the published artifact shape) and proves the new inspect
// extractor recovers the same ordered words that the source
// transcript.words.v1.json carried — i.e. the published .opus actually embeds
// the expected transcript, not just non-empty audio. This is the Go-side guard
// the Talk roundtrip phase-9 check now relies on.
func TestExtractTranscriptWordsFromPackedOpus(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingDir := filepath.Join(tmp, "good.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	want := sourceTranscriptWords(t, filepath.Join(meetingDir, "transcript.words.v1.json"))
	if len(want) == 0 {
		t.Fatalf("fixture transcript carried no words")
	}

	opusPath := filepath.Join(tmp, "good.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, opusPath, portablePackOptions{Title: "Daily Standup"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}

	extracted, err := inspectpkg.ExtractTranscriptWords(opusPath)
	if err != nil {
		t.Fatalf("ExtractTranscriptWords(%q): %v", opusPath, err)
	}

	got := make([]string, 0, len(extracted.Words))
	for _, w := range extracted.Words {
		got = append(got, w.Text)
	}
	if len(got) != len(want) {
		t.Fatalf("recovered %d words %v, want %d words %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recovered words mismatch at %d: got %q want %q (full got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}

	// The rendered transcript.words.v1.json must round-trip through the same
	// segments[].words[] reader the phase-9 roundtrip check uses.
	var rendered bytes.Buffer
	if err := inspectpkg.WriteTranscriptWordsV1JSON(&rendered, extracted); err != nil {
		t.Fatalf("WriteTranscriptWordsV1JSON: %v", err)
	}
	renderedPath := filepath.Join(tmp, "recovered.words.v1.json")
	if err := os.WriteFile(renderedPath, rendered.Bytes(), 0o644); err != nil {
		t.Fatalf("write rendered transcript: %v", err)
	}
	renderedWords := sourceTranscriptWords(t, renderedPath)
	if len(renderedWords) != len(want) {
		t.Fatalf("rendered words %v != source words %v", renderedWords, want)
	}
	for i := range want {
		if renderedWords[i] != want[i] {
			t.Fatalf("rendered word mismatch at %d: got %q want %q", i, renderedWords[i], want[i])
		}
	}
}

// TestExtractTranscriptWordsCLIDumpsTranscript exercises the CLI surface:
// `cassini inspect --transcript <opus>` writes the recovered transcript to
// stdout and leaves the default inspect output untouched.
func TestExtractTranscriptWordsCLIDumpsTranscript(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingDir := filepath.Join(tmp, "good.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	want := sourceTranscriptWords(t, filepath.Join(meetingDir, "transcript.words.v1.json"))

	opusPath := filepath.Join(tmp, "good.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, opusPath, portablePackOptions{Title: "Daily Standup"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"inspect", "--transcript", opusPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("inspect --transcript exit=%d stderr=%q", code, stderr.String())
	}

	tmpFile := filepath.Join(tmp, "cli-out.words.v1.json")
	if err := os.WriteFile(tmpFile, stdout.Bytes(), 0o644); err != nil {
		t.Fatalf("write cli output: %v", err)
	}
	got := sourceTranscriptWords(t, tmpFile)
	if len(got) != len(want) {
		t.Fatalf("cli recovered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cli word mismatch at %d: got %q want %q", i, got[i], want[i])
		}
	}
}
