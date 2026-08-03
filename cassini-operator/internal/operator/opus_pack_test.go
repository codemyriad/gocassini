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

// seedAttemptMeeting creates a minimal built `.meeting` bundle for one attempt.
func seedAttemptMeeting(t *testing.T, workRoot, jobID string, attemptNumber int) string {
	t.Helper()
	meetingPath := attemptMeetingPath(workRoot, jobID, attemptNumber)
	if err := os.MkdirAll(meetingPath, 0o755); err != nil {
		t.Fatalf("mkdir meeting: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingPath, "cassini.json"), []byte(`{"kind":"meeting"}`), 0o644); err != nil {
		t.Fatalf("write meeting manifest: %v", err)
	}
	return meetingPath
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

func TestPackAttemptMeetingToOpusWritesTheAttemptScopedOpus(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	meetingPath := seedAttemptMeeting(t, workRoot, jobID, 1)

	// Fake `cassini pack <meeting> --out <opus>`: assert it is invoked on this
	// attempt's meeting and write the requested .opus so the success path is
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

	opusPath, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "", nil)
	if err != nil {
		t.Fatalf("packAttemptMeetingToOpus() error = %v", err)
	}
	if opusPath != attemptOpusPath(workRoot, jobID, 1) {
		t.Fatalf("opusPath = %q, want %q", opusPath, attemptOpusPath(workRoot, jobID, 1))
	}
	if _, err := os.Stat(opusPath); err != nil {
		t.Fatalf("expected .opus written: %v", err)
	}
	// The `.meeting` bundle must remain untouched: it stays as attempt-scoped
	// retry/debug material until D-425's retention decision retires it.
	if _, err := os.Stat(meetingPath); err != nil {
		t.Fatalf("attempt .meeting must survive packing: %v", err)
	}
	// The seal lands in the attempt's own directory under runs/, beside the
	// attempt it belongs to — not in current/, where two attempts of one job
	// would collide (D-583). Its file name stays the job id, because the
	// exporter derives the catalog id from the stem.
	if filepath.Dir(opusPath) != attemptSealDir(workRoot, jobID, 1) {
		t.Fatalf("sealed opus must live in the attempt seal dir, got %q", opusPath)
	}
	if filepath.Base(opusPath) != jobID+".opus" {
		t.Fatalf("sealed opus must be named for the job, got %q", filepath.Base(opusPath))
	}
}

// Two attempts of one job seal two separate files. Before D-583 both packs
// wrote current/<job>.opus and the winner was whichever finished last.
func TestPackAttemptMeetingToOpusKeepsAttemptsApart(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedAttemptMeeting(t, workRoot, jobID, 1)
	seedAttemptMeeting(t, workRoot, jobID, 2)
	bin := writeFakeCassini(t, `printf '%s' "$2" > "$4"; exit 0`)

	first, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "", nil)
	if err != nil {
		t.Fatalf("seal attempt 1: %v", err)
	}
	second, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 2), attemptOpusPath(workRoot, jobID, 2), "", nil)
	if err != nil {
		t.Fatalf("seal attempt 2: %v", err)
	}
	if first == second {
		t.Fatalf("two attempts must not seal the same path, both = %q", first)
	}
	firstBody, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read attempt 1 seal: %v", err)
	}
	if string(firstBody) != attemptMeetingPath(workRoot, jobID, 1) {
		t.Fatalf("attempt 1 seal was overwritten: %q", string(firstBody))
	}
}

func TestPackAttemptMeetingToOpusPassesTitle(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedAttemptMeeting(t, workRoot, jobID, 1)

	// A resolved Talk room name must reach `cassini pack` as --title so the
	// packed meeting carries the human-readable name (D-462).
	bin := writeFakeCassini(t, `
if [ "$5" != "--title" ]; then echo "missing --title, got $5" >&2; exit 9; fi
if [ "$6" != "Daily Meeting" ]; then echo "wrong title: $6" >&2; exit 9; fi
printf 'opus-bytes' > "$4"
exit 0
`)

	if _, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "Daily Meeting", nil); err != nil {
		t.Fatalf("packAttemptMeetingToOpus() error = %v", err)
	}
}

func TestPackAttemptMeetingToOpusOmitsEmptyTitle(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedAttemptMeeting(t, workRoot, jobID, 1)

	bin := writeFakeCassini(t, `
if [ "$#" != "4" ]; then echo "unexpected extra args: $#" >&2; exit 9; fi
printf 'opus-bytes' > "$4"
exit 0
`)

	if _, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "   ", nil); err != nil {
		t.Fatalf("packAttemptMeetingToOpus() error = %v", err)
	}
}

func TestPackAttemptMeetingToOpusErrorsWhenMeetingMissing(t *testing.T) {
	workRoot := t.TempDir()
	bin := writeFakeCassini(t, "exit 0\n")
	if _, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, "missing", 1), attemptOpusPath(workRoot, "missing", 1), "", nil); err == nil {
		t.Fatal("expected error when the attempt meeting bundle is absent")
	}
}

func TestPackAttemptMeetingToOpusErrorsWhenCommandFails(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedAttemptMeeting(t, workRoot, jobID, 1)

	// Simulate a cassini binary that predates `cassini pack` (unknown verb,
	// exit 2). This used to be survivable — `.meeting` was still the publish
	// input — and is now a seal failure, because nothing falls back to it.
	bin := writeFakeCassini(t, "echo 'unknown command \"pack\"' >&2\nexit 2\n")
	if _, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "", nil); err == nil {
		t.Fatal("expected error when cassini pack fails")
	}
	// No partial .opus should be left claiming success.
	if _, err := os.Stat(attemptOpusPath(workRoot, jobID, 1)); err == nil {
		t.Fatal("no .opus should exist after a failed pack")
	}
}

func TestPackAttemptMeetingToOpusErrorsWhenOutputMissing(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedAttemptMeeting(t, workRoot, jobID, 1)

	// A cassini that exits 0 but writes nothing must be treated as a failure
	// so we never report a phantom sealed artifact.
	bin := writeFakeCassini(t, "exit 0\n")
	_, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "", nil)
	if err == nil || !strings.Contains(err.Error(), "pack output missing") {
		t.Fatalf("expected pack output missing error, got %v", err)
	}
}

// A zero-byte `.opus` passes every later existence check and fails only in the
// viewer. Catch it while it is still a seal failure.
func TestPackAttemptMeetingToOpusErrorsWhenOutputIsEmpty(t *testing.T) {
	workRoot := t.TempDir()
	jobID := "job1"
	seedAttemptMeeting(t, workRoot, jobID, 1)

	bin := writeFakeCassini(t, ": > \"$4\"\nexit 0\n")
	_, err := packAttemptMeetingToOpus(context.Background(), bin, attemptMeetingPath(workRoot, jobID, 1), attemptOpusPath(workRoot, jobID, 1), "", nil)
	if err == nil || !strings.Contains(err.Error(), "pack output is empty") {
		t.Fatalf("expected empty pack output error, got %v", err)
	}
}

// A seal task with no meeting bundle recorded has nothing to pack. It cannot
// reach the packer through the dispatcher (ListQueuedSealTasks filters the
// column out) but the guard is what makes that a contract rather than a
// coincidence.
func TestPackAttemptMeetingToOpusErrorsWithoutAMeetingPath(t *testing.T) {
	workRoot := t.TempDir()
	bin := writeFakeCassini(t, "exit 0\n")
	_, err := packAttemptMeetingToOpus(context.Background(), bin, "   ", attemptOpusPath(workRoot, "job1", 1), "", nil)
	if err == nil || !strings.Contains(err.Error(), "no meeting bundle to seal") {
		t.Fatalf("expected a missing-meeting-path error, got %v", err)
	}
}

// The seal packs the bundle its task carries, not one re-derived from the job
// id and attempt number. The two agree in production; the point is that the
// recorded value is what is honoured.
func TestPackAttemptMeetingToOpusPacksTheBundleItIsGiven(t *testing.T) {
	workRoot := t.TempDir()
	// A bundle that does not follow the attempt naming convention at all.
	elsewhere := filepath.Join(t.TempDir(), "recovered.meeting")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "cassini.json"), []byte(`{"kind":"meeting"}`), 0o644); err != nil {
		t.Fatalf("write bundle manifest: %v", err)
	}
	bin := writeFakeCassini(t, `printf '%s' "$2" > "$4"; exit 0`)

	opusPath, err := packAttemptMeetingToOpus(context.Background(), bin, elsewhere, attemptOpusPath(workRoot, "job1", 1), "", nil)
	if err != nil {
		t.Fatalf("packAttemptMeetingToOpus() error = %v", err)
	}
	body, err := os.ReadFile(opusPath)
	if err != nil {
		t.Fatalf("read sealed opus: %v", err)
	}
	if string(body) != elsewhere {
		t.Fatalf("packed %q, want the bundle the task carried (%q)", string(body), elsewhere)
	}
}
