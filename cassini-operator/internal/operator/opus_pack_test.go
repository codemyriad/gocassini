package operator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCassini writes an executable shell script that stands in for the
// cassini CLI binary. The script body decides how the fake `cassini pack`
// behaves so each test can exercise success and failure paths without ffmpeg.
func writeFakeCassini(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "cassini")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake cassini: %v", err)
	}
	return script
}

// seedPromotedMeeting creates a minimal promoted `.meeting` bundle in current/.
func seedPromotedMeeting(t *testing.T, workRoot, jobID string) string {
	t.Helper()
	meetingPath := canonicalMeetingPath(workRoot, jobID)
	if err := os.MkdirAll(meetingPath, 0o755); err != nil {
		t.Fatalf("mkdir meeting: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingPath, "cassini.json"), []byte(`{"kind":"meeting"}`), 0o644); err != nil {
		t.Fatalf("write meeting manifest: %v", err)
	}
	return meetingPath
}

func TestPackCanonicalMeetingToOpusWritesOpusBesideMeeting(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	meetingPath := seedPromotedMeeting(t, workRoot, jobID)

	// Fake `cassini pack <meeting> --out <opus>`: assert it is invoked on the
	// promoted meeting and write the requested .opus so the success path is
	// exercised end to end.
	bin := writeFakeCassini(t, `
if [ "$1" != "pack" ]; then echo "unexpected verb $1" >&2; exit 9; fi
meeting="$2"
if [ "$3" != "--out" ]; then echo "missing --out" >&2; exit 9; fi
out="$4"
if [ ! -d "$meeting" ]; then echo "meeting not a dir: $meeting" >&2; exit 9; fi
printf 'opus-bytes' > "$out"
exit 0
`)

	opusPath, err := packCanonicalMeetingToOpus(context.Background(), bin, workRoot, jobID, nil)
	if err != nil {
		t.Fatalf("packCanonicalMeetingToOpus() error = %v", err)
	}
	if opusPath != canonicalOpusPath(workRoot, jobID) {
		t.Fatalf("opusPath = %q, want %q", opusPath, canonicalOpusPath(workRoot, jobID))
	}
	if _, err := os.Stat(opusPath); err != nil {
		t.Fatalf("expected .opus written: %v", err)
	}
	// The .meeting bundle must remain untouched: the .opus is additive (D-428).
	if _, err := os.Stat(meetingPath); err != nil {
		t.Fatalf("promoted .meeting must survive packing: %v", err)
	}
	// The .opus lands in current/, alongside the .meeting.
	if filepath.Dir(opusPath) != currentRoot(workRoot) {
		t.Fatalf("opus must live in current/, got %q", opusPath)
	}
}

func TestPackCanonicalMeetingToOpusErrorsWhenMeetingMissing(t *testing.T) {
	workRoot := t.TempDir()
	bin := writeFakeCassini(t, "exit 0\n")
	if _, err := packCanonicalMeetingToOpus(context.Background(), bin, workRoot, "missing", nil); err == nil {
		t.Fatal("expected error when promoted meeting is absent")
	}
}

func TestPackCanonicalMeetingToOpusErrorsWhenCommandFails(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedPromotedMeeting(t, workRoot, jobID)

	// Simulate a cassini binary that predates `cassini pack` (unknown verb,
	// exit 2) — the operator must surface a non-nil error so the caller logs a
	// skip, but the build itself stays unaffected.
	bin := writeFakeCassini(t, "echo 'unknown command \"pack\"' >&2\nexit 2\n")
	if _, err := packCanonicalMeetingToOpus(context.Background(), bin, workRoot, jobID, nil); err == nil {
		t.Fatal("expected error when cassini pack fails")
	}
	// No partial .opus should be left claiming success.
	if _, err := os.Stat(canonicalOpusPath(workRoot, jobID)); err == nil {
		t.Fatal("no .opus should exist after a failed pack")
	}
}

func TestPackCanonicalMeetingToOpusErrorsWhenOutputMissing(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedPromotedMeeting(t, workRoot, jobID)

	// A cassini that exits 0 but writes nothing must be treated as a failure
	// so we never report a phantom durable artifact.
	bin := writeFakeCassini(t, "exit 0\n")
	_, err := packCanonicalMeetingToOpus(context.Background(), bin, workRoot, jobID, nil)
	if err == nil || !strings.Contains(err.Error(), "pack output missing") {
		t.Fatalf("expected pack output missing error, got %v", err)
	}
}
