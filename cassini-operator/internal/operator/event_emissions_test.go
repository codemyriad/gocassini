package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedBuildForEventTest(t *testing.T, rt *Runtime, id string) (Job, string) {
	t.Helper()
	job := insertDisposalJob(t, rt, id, "build", "queued")
	run := seedReadyRunBundle(t, rt.cfg.WorkRoot, job.ID)
	if err := rt.store.MarkBuildQueued(context.Background(), job.ID, run, run, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	return job, run
}

func attachEventWebhookForTest(t *testing.T, rt *Runtime, events string) {
	t.Helper()
	hook := newTestEventWebhook(t, rt.store, "https://hooks.example/events", "secret", events)
	rt.eventWebhookMu.Lock()
	rt.eventWebhook = hook
	rt.eventWebhookMu.Unlock()
}

func writeCompletedMeetingForEventTest(t *testing.T, meetingPath string) {
	t.Helper()
	if err := os.MkdirAll(meetingPath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"processing": map[string]any{
			"meetingSummary": map[string]any{"status": "completed", "model": "summary-model"},
		},
		"files": map[string]any{"summary": "summary.md"},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(meetingPath, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meetingPath, "summary.md"), []byte("# Summary\n\nWritten.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func eventOutboxTypes(t *testing.T, store *Store) []string {
	t.Helper()
	rows, err := store.db.Query(`SELECT type FROM event_outbox ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		types = append(types, eventType)
	}
	return types
}

// A normal build writes its summary inside `cassini build`; the event must come
// from the build's step outcomes, not only from the summarize endpoint.
func TestNormalBuildEmitsSummaryCompleted(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	job, run := seedBuildForEventTest(t, rt, "summary-build")
	attachEventWebhookForTest(t, rt, eventSummaryCompleted+","+eventBuildCompleted)
	rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		writeCompletedMeetingForEventTest(t, meetingPath)
		return meetingPath, nil
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

	types := eventOutboxTypes(t, rt.store)
	if !containsEvent(types, eventSummaryCompleted) {
		t.Fatalf("events = %v, want summary.completed from a normal build", types)
	}
	var payload string
	if err := rt.store.db.QueryRow(`SELECT payload FROM event_outbox WHERE type = ?`, eventSummaryCompleted).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "summary_markdown") || !strings.Contains(payload, "Written.") {
		t.Fatalf("summary payload = %s, want the Markdown body", payload)
	}
}

// A summary step that failed inside an otherwise successful build emits
// summary.failed, not recording.failed.
func TestNormalBuildSummaryFailureEmitsSummaryFailedOnly(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	job, run := seedBuildForEventTest(t, rt, "summary-failed")
	attachEventWebhookForTest(t, rt, eventSummaryFailed+","+eventRecordingFailed)
	rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := os.MkdirAll(meetingPath, 0o755); err != nil {
			return "", err
		}
		manifest := map[string]any{"processing": map[string]any{
			"meetingSummary": map[string]any{"status": "failed", "reason": "upstream 502"},
		}}
		raw, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(meetingPath, "manifest.json"), raw, 0o600); err != nil {
			return "", err
		}
		return meetingPath, nil
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

	types := eventOutboxTypes(t, rt.store)
	if !containsEvent(types, eventSummaryFailed) {
		t.Fatalf("events = %v, want summary.failed", types)
	}
	if containsEvent(types, eventRecordingFailed) {
		t.Fatalf("events = %v, a failed summary step must not emit recording.failed", types)
	}
}

// A summarize attempt that genuinely fails is a summary failure. It is not a
// recording failure: the published recording is intact.
func TestSummarizeAttemptFailureDoesNotEmitRecordingFailed(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	job, run := seedBuildForEventTest(t, rt, "summarize-failed")
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET trigger_kind = ? WHERE job_id = ? AND attempt_number = 1`, triggerKindSummarize, job.ID); err != nil {
		t.Fatal(err)
	}
	attachEventWebhookForTest(t, rt, eventSummaryFailed+","+eventRecordingFailed)
	rt.buildJobFn = func(context.Context, buildTask) (string, error) {
		return "", contextError("model returned 502")
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

	types := eventOutboxTypes(t, rt.store)
	if !containsEvent(types, eventSummaryFailed) {
		t.Fatalf("events = %v, want summary.failed", types)
	}
	if containsEvent(types, eventRecordingFailed) {
		t.Fatalf("events = %v, a summarize attempt must not emit recording.failed", types)
	}
}

// Temporary resource pressure defers the attempt; that is not a failure and
// must not produce either summary.failed or recording.failed.
func TestDeferredBuildDoesNotEmitFailureEvents(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	job, run := seedBuildForEventTest(t, rt, "deferred-summary")
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET trigger_kind = ? WHERE job_id = ? AND attempt_number = 1`, triggerKindSummarize, job.ID); err != nil {
		t.Fatal(err)
	}
	attachEventWebhookForTest(t, rt, eventSummaryFailed+","+eventRecordingFailed)
	rt.buildJobFn = func(context.Context, buildTask) (string, error) {
		return "", &resourceUnavailableError{resource: "memory", detail: "under pressure", permanent: false}
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

	types := eventOutboxTypes(t, rt.store)
	if len(types) != 0 {
		t.Fatalf("events = %v, a deferred build must emit no failure event", types)
	}
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

// contextError is a plain error; the name keeps the fixture readable.
func contextError(text string) error { return &eventTestError{text: text} }

type eventTestError struct{ text string }

func (e *eventTestError) Error() string { return e.text }
