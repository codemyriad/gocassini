package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArtifactManifest(t *testing.T, meetingPath string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(meetingPath, "manifest.json")
	manifest := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	} else if err := os.MkdirAll(meetingPath, 0o755); err != nil {
		t.Fatal(err)
	}
	edit(manifest)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadBundleStepOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest map[string]any
		want     stepOutcomes
	}{
		{
			name: "recorded",
			manifest: map[string]any{"processing": map[string]any{
				"transcription":  map[string]any{"status": "failed", "reason": "insufficient_resources"},
				"meetingSummary": map[string]any{"status": "skipped", "reason": "no_transcript"},
			}},
			want: stepOutcomes{TranscriptionStatus: "failed", TranscriptionReason: "insufficient_resources", SummaryStatus: "skipped", SummaryReason: "no_transcript"},
		},
		{
			// Built before the summary outcome was recorded, with a summary.
			name: "older bundle with a summary",
			manifest: map[string]any{
				"files":      map[string]any{"summary": "summary.md"},
				"processing": map[string]any{"transcription": map[string]any{"status": "completed"}},
			},
			want: stepOutcomes{TranscriptionStatus: "completed", SummaryStatus: "completed"},
		},
		{
			// Says nothing at all: nothing is invented.
			name:     "older bundle without processing",
			manifest: map[string]any{"files": map[string]any{"audio": "meeting.webm"}},
			want:     stepOutcomes{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeArtifactManifest(t, dir, func(m map[string]any) {
				for k, v := range tc.manifest {
					m[k] = v
				}
			})
			got, err := readBundleStepOutcomes(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("outcomes = %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := readBundleStepOutcomes(t.TempDir()); err == nil {
		t.Fatal("a bundle without manifest.json read as having no outcomes")
	}
}

func TestCrashDetailNamesTheCause(t *testing.T) {
	tail := "loading model\nCUDA error: out of memory (allocating 2 GiB)\nterminate called\nAborted\n"
	if got := crashDetail(tail, errors.New("exit status 134")); got != "CUDA error: out of memory (allocating 2 GiB)" {
		t.Errorf("crashDetail() = %q, want the out-of-memory line", got)
	}
	if got := crashDetail("step 3\nsegfault in decoder\n\n", nil); got != "segfault in decoder" {
		t.Errorf("crashDetail() = %q, want the last line", got)
	}
	if got := crashDetail("", errors.New("exit status 139")); got != "exit status 139" {
		t.Errorf("crashDetail() = %q, want the process error", got)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	tail := newTailBuffer(8)
	_, _ = tail.Write([]byte("0123456789"))
	_, _ = tail.Write([]byte("ab"))
	if got := tail.String(); got != "456789ab" {
		t.Fatalf("tail = %q, want the last 8 bytes", got)
	}
	long := strings.Repeat("x", maxTranscriptionDetail+50)
	if got := boundTranscriptionDetail(long); len(got) > maxTranscriptionDetail+len("…") || !strings.HasPrefix(got, "…") {
		t.Fatalf("bounded detail = %d bytes, want at most %d marked as cut", len(got), maxTranscriptionDetail)
	}
}

// A run that published without a transcript or a summary says so on the
// attempt the Operator shows (D-854, D-814).
func TestRunBuildJobRecordsStepOutcomes(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	const jobID = "job-outcomes"
	runPath := seedReadyRunBundle(t, rt.cfg.WorkRoot, jobID)
	insertJob(t, rt.store.db, jobID, nowUTCString())
	if err := rt.store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		writeArtifactManifest(t, meetingPath, func(m map[string]any) {
			m["processing"] = map[string]any{
				"transcription":  map[string]any{"status": "failed", "reason": "transcription_failed"},
				"meetingSummary": map[string]any{"status": "skipped", "reason": "no_transcript"},
			}
		})
		return meetingPath, nil
	}

	rt.runBuildJob(buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath}, 1)
	waitForJobState(t, rt.store, jobID, "succeeded")

	attempt, err := rt.store.GetJobAttempt(context.Background(), jobID, 1)
	if err != nil {
		t.Fatalf("GetJobAttempt() error = %v", err)
	}
	if attempt.TranscriptionStatus != "failed" || attempt.TranscriptionReason != "transcription_failed" ||
		attempt.SummaryStatus != "skipped" || attempt.SummaryReason != "no_transcript" {
		t.Fatalf("attempt outcomes = %s/%s, %s/%s", attempt.TranscriptionStatus, attempt.TranscriptionReason, attempt.SummaryStatus, attempt.SummaryReason)
	}
	// And they reach the job detail the Operator reads.
	raw, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"transcription_status":"failed"`) || !strings.Contains(string(raw), `"summary_reason":"no_transcript"`) {
		t.Fatalf("job detail JSON lacks the outcomes: %s", raw)
	}
}

// A meeting published before outcomes were recorded still shows them: the
// job detail reads the published bundle for that attempt, and only for it.
func TestFillPublishedStepOutcomesReadsThePublishedBundle(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	const jobID = "job-older"
	writeArtifactManifest(t, canonicalMeetingPath(rt.cfg.WorkRoot, jobID), func(m map[string]any) {
		m["processing"] = map[string]any{"transcription": map[string]any{"status": "failed", "reason": "transcription_failed"}}
	})
	attempts := []JobAttempt{
		{JobID: jobID, AttemptNumber: 2},
		{JobID: jobID, AttemptNumber: 1},
		{JobID: jobID, AttemptNumber: 3, TranscriptionStatus: "completed"},
	}
	rt.fillPublishedStepOutcomes(jobID, 2, attempts)
	if attempts[0].TranscriptionStatus != "failed" || attempts[0].TranscriptionReason != "transcription_failed" {
		t.Fatalf("published attempt = %+v, want the bundle's outcome", attempts[0])
	}
	if attempts[1].TranscriptionStatus != "" {
		t.Fatalf("an unpublished attempt was filled from the published bundle: %+v", attempts[1])
	}

	// A recorded outcome is the attempt's own and is not overwritten.
	rt.fillPublishedStepOutcomes(jobID, 3, attempts)
	if attempts[2].TranscriptionStatus != "completed" {
		t.Fatalf("recorded outcome overwritten: %+v", attempts[2])
	}
	// No published bundle on disk: nothing filled, nothing fails.
	none := []JobAttempt{{JobID: "job-none", AttemptNumber: 1}}
	rt.fillPublishedStepOutcomes("job-none", 1, none)
	if none[0].TranscriptionStatus != "" {
		t.Fatalf("filled from nowhere: %+v", none[0])
	}
}
