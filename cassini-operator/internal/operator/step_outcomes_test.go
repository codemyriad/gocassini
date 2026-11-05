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
			// The models that ran, and the device the speech model ran on (D-816).
			name: "models",
			manifest: map[string]any{
				"processing": map[string]any{
					"transcription":  map[string]any{"status": "completed", "model": "parakeet-tdt-0.6b-v3", "revision": "4a1c2e9"},
					"meetingSummary": map[string]any{"status": "completed", "model": "gpt-4.1-mini"},
				},
				"provenance": map[string]any{"speechToText": map[string]any{"backend": "sherpa-onnx", "device": "cuda"}},
			},
			want: stepOutcomes{
				TranscriptionStatus: "completed", TranscriptionModel: "parakeet-tdt-0.6b-v3", TranscriptionRevision: "4a1c2e9", TranscriptionDevice: "cuda",
				SummaryStatus: "completed", SummaryModel: "gpt-4.1-mini",
			},
		},
		{
			// When the summary's model call ran (D-899).
			name: "summary times",
			manifest: map[string]any{"processing": map[string]any{
				"transcription":  map[string]any{"status": "completed"},
				"meetingSummary": map[string]any{"status": "completed", "model": "gpt-4.1-mini", "startedAt": "2026-11-02T10:00:00.000Z", "finishedAt": "2026-11-02T10:00:20.500Z"},
			}},
			want: stepOutcomes{
				TranscriptionStatus: "completed", SummaryStatus: "completed", SummaryModel: "gpt-4.1-mini",
				SummaryStartedAt: "2026-11-02T10:00:00.000Z", SummaryFinishedAt: "2026-11-02T10:00:20.500Z",
			},
		},
		{
			// No model ran, so no device is claimed for one.
			name: "transcription skipped",
			manifest: map[string]any{
				"processing": map[string]any{"transcription": map[string]any{"status": "skipped", "reason": "disabled"}},
				"provenance": map[string]any{"speechToText": map[string]any{"device": "cpu"}},
			},
			want: stepOutcomes{TranscriptionStatus: "skipped", TranscriptionReason: "disabled"},
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

// The summary's times are the ones this attempt wrote (D-899): a build's from
// its bundle, a speaker-edit rewrite's from its report, and never, for a
// refine, the bundle's, which an earlier attempt wrote.
func TestRecordAttemptStepOutcomesKeepsTheSummaryThisAttemptWrote(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	const jobID = "job-summary-times"
	insertJob(t, rt.store.db, jobID, nowUTCString())
	if err := rt.store.MarkBuildQueued(ctx, jobID, "/runs/job.run", "/runs/job.run", nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	built := stepOutcomes{SummaryStatus: "completed", SummaryStartedAt: "2026-11-02T10:00:00.000Z", SummaryFinishedAt: "2026-11-02T10:00:20.000Z"}
	times := func(n int) (string, string) {
		t.Helper()
		a, err := rt.store.GetJobAttempt(ctx, jobID, n)
		if err != nil {
			t.Fatalf("GetJobAttempt(%d) error = %v", n, err)
		}
		return a.SummaryStartedAt, a.SummaryFinishedAt
	}

	// A build: the bundle's.
	if err := rt.store.RecordAttemptStepOutcomes(ctx, jobID, 1, built); err != nil {
		t.Fatal(err)
	}
	if start, finish := times(1); start != built.SummaryStartedAt || finish != built.SummaryFinishedAt {
		t.Fatalf("build's summary times = %s..%s, want the bundle's", start, finish)
	}

	// A rerun whose replay rewrote the summary: from the build's call to the
	// rewrite's end, both of which it paid for.
	if err := rt.store.SetAttemptSummaryTimes(ctx, jobID, 1, "2026-11-02T10:00:25.000Z", "2026-11-02T10:00:40.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.RecordAttemptStepOutcomes(ctx, jobID, 1, built); err != nil {
		t.Fatal(err)
	}
	if start, finish := times(1); start != built.SummaryStartedAt || finish != "2026-11-02T10:00:40.000Z" {
		t.Fatalf("summary times after a rewrite = %s..%s, want the build's call to the rewrite's end", start, finish)
	}

	// A refine that kept the summary: none, though its bundle names some.
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET trigger_kind = ?, summary_started_at = '', summary_finished_at = '' WHERE job_id = ?`, triggerKindRefine, jobID); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.RecordAttemptStepOutcomes(ctx, jobID, 1, built); err != nil {
		t.Fatal(err)
	}
	if start, finish := times(1); start != "" || finish != "" {
		t.Fatalf("a refine took the bundle's summary times %s..%s, which an earlier attempt wrote", start, finish)
	}
}

func TestAttemptSummaryTimes(t *testing.T) {
	const b0, b1, r0, r1 = "b0", "b1", "r0", "r1"
	for _, tc := range []struct {
		name, kind, bs, bf, rs, rf, wantS, wantF string
	}{
		{"build", "initial", b0, b1, "", "", b0, b1},
		{"build that wrote none", "initial", "", "", "", "", "", ""},
		{"rerun that rewrote it", triggerKindRerun, b0, b1, r0, r1, b0, r1},
		{"rerun whose build wrote none", triggerKindRerun, "", "", r0, r1, r0, r1},
		{"refine that rewrote it", triggerKindRefine, b0, b1, r0, r1, r0, r1},
		{"refine that kept it", triggerKindRefine, b0, b1, "", "", "", ""},
	} {
		if s, f := attemptSummaryTimes(tc.kind, tc.bs, tc.bf, tc.rs, tc.rf); s != tc.wantS || f != tc.wantF {
			t.Errorf("%s: times = %q..%q, want %q..%q", tc.name, s, f, tc.wantS, tc.wantF)
		}
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

// An attempt names the models that made its meeting (D-816).
func TestRunBuildJobRecordsModels(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	const jobID = "job-models"
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
				"transcription":  map[string]any{"status": "completed", "model": "parakeet-tdt-0.6b-v3", "revision": "4a1c2e9"},
				"meetingSummary": map[string]any{"status": "completed", "model": "gpt-4.1-mini"},
			}
			m["provenance"] = map[string]any{"speechToText": map[string]any{"device": "cuda"}}
		})
		return meetingPath, nil
	}

	rt.runBuildJob(buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath}, 1)
	waitForJobState(t, rt.store, jobID, "succeeded")

	attempt, err := rt.store.GetJobAttempt(context.Background(), jobID, 1)
	if err != nil {
		t.Fatalf("GetJobAttempt() error = %v", err)
	}
	if attempt.TranscriptionModel != "parakeet-tdt-0.6b-v3" || attempt.TranscriptionRevision != "4a1c2e9" ||
		attempt.TranscriptionDevice != "cuda" || attempt.SummaryModel != "gpt-4.1-mini" {
		t.Fatalf("attempt models = %q %q %q, summary %q", attempt.TranscriptionModel, attempt.TranscriptionRevision, attempt.TranscriptionDevice, attempt.SummaryModel)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].SummaryModel != "gpt-4.1-mini" {
		t.Fatalf("listed attempts lack the models: %+v", attempts)
	}
}

// An attempt published before models were recorded names them from the
// bundle it published, without its recorded outcomes being replaced.
func TestFillPublishedStepOutcomesAddsModels(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	const jobID = "job-models-older"
	writeArtifactManifest(t, canonicalMeetingPath(rt.cfg.WorkRoot, jobID), func(m map[string]any) {
		m["processing"] = map[string]any{
			"transcription":  map[string]any{"status": "completed", "model": "parakeet-tdt-0.6b-v3"},
			"meetingSummary": map[string]any{"status": "failed", "reason": "llm_failed"},
		}
		m["provenance"] = map[string]any{"speechToText": map[string]any{"device": "cpu"}}
	})
	attempts := []JobAttempt{{JobID: jobID, AttemptNumber: 1, TranscriptionStatus: "completed", SummaryStatus: "completed"}}
	rt.fillPublishedStepOutcomes(jobID, 1, attempts)
	got := attempts[0]
	if got.TranscriptionModel != "parakeet-tdt-0.6b-v3" || got.TranscriptionDevice != "cpu" {
		t.Fatalf("published attempt = %+v, want the bundle's models", got)
	}
	if got.SummaryStatus != "completed" {
		t.Fatalf("recorded outcome replaced by the bundle's: %+v", got)
	}
}

// A speaker-edit replay whose rewrite the built-in template wrote marks the
// attempt's summary as a template fallback, and recording the bundle's own
// outcomes afterwards keeps it: summary.md is the rewrite (D-819).
func TestRecordAttemptStepOutcomesKeepsAReplaysTemplateFallback(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	const jobID = "job-summary-fallback"
	insertJob(t, rt.store.db, jobID, nowUTCString())
	if err := rt.store.MarkBuildQueued(ctx, jobID, "/runs/job.run", "/runs/job.run", nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	if err := rt.store.SetAttemptSummaryReplayReason(ctx, jobID, 1, summaryReasonTemplateFallback); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.RecordAttemptStepOutcomes(ctx, jobID, 1, stepOutcomes{SummaryStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	a, err := rt.store.GetJobAttempt(ctx, jobID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.SummaryStatus != "completed" || a.SummaryReason != summaryReasonTemplateFallback {
		t.Errorf("summary = %q/%q, want completed/%s", a.SummaryStatus, a.SummaryReason, summaryReasonTemplateFallback)
	}
}

// A refine copies an earlier bundle whose processing block may still say the
// built-in wrote its summary. When the refine rewrote the summary with the
// configured template, that stale fallback is not the meeting's any more.
func TestRecordAttemptStepOutcomesClearsAFallbackTheRewriteDidNotHave(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	const jobID = "job-summary-fallback-cleared"
	insertJob(t, rt.store.db, jobID, nowUTCString())
	if err := rt.store.MarkBuildQueued(ctx, jobID, "/runs/job.run", "/runs/job.run", nowUTCString()); err != nil {
		t.Fatalf("MarkBuildQueued() error = %v", err)
	}
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET trigger_kind = ? WHERE job_id = ?`, triggerKindRefine, jobID); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.SetAttemptSummaryTimes(ctx, jobID, 1, "2026-11-05T10:00:00Z", "2026-11-05T10:00:05Z"); err != nil {
		t.Fatal(err)
	}
	outcomes := stepOutcomes{SummaryStatus: "completed", SummaryReason: summaryReasonTemplateFallback}
	if err := rt.store.RecordAttemptStepOutcomes(ctx, jobID, 1, outcomes); err != nil {
		t.Fatal(err)
	}
	a, err := rt.store.GetJobAttempt(ctx, jobID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.SummaryStatus != "completed" || a.SummaryReason != "" {
		t.Errorf("summary = %q/%q, want completed with no reason", a.SummaryStatus, a.SummaryReason)
	}
}
