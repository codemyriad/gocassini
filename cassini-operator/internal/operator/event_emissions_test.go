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

func setAttemptKindForEventTest(t *testing.T, rt *Runtime, jobID, kind string) {
	t.Helper()
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET trigger_kind = ? WHERE job_id = ? AND attempt_number = 1`, kind, jobID); err != nil {
		t.Fatal(err)
	}
}

// Past the build stage a summarize or speaker-edit attempt still works on a
// copy of a meeting that is already published. Its failure at seal, at
// publish or across a restart is never a lost recording.
func TestAttemptFailureEventFollowsWhatTheAttemptWasFor(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want []string
	}{
		{kind: triggerKindSummarize, want: []string{eventSummaryFailed}},
		{kind: triggerKindRefine, want: nil},
		{kind: triggerKindRerun, want: []string{eventRecordingFailed}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			rt, cleanup := newBareSealRuntime(t)
			defer cleanup()
			job, _ := seedBuildForEventTest(t, rt, "attempt-kind")
			setAttemptKindForEventTest(t, rt, job.ID, tc.kind)
			attachEventWebhookForTest(t, rt, eventSummaryFailed+","+eventRecordingFailed)

			rt.failSeal(sealTask{JobID: job.ID, AttemptNumber: 1}, "", contextError("ffmpeg exited 1"), nowUTCString())
			rt.emitAttemptFailure(job.ID, 1, "publish", "upload refused", nil)
			// What the startup sweep sends for a job a restart interrupted.
			rt.emitAttemptFailure(job.ID, 1, "build", "operator restarted before the stage finished", map[string]any{"interrupted": true})

			types := eventOutboxTypes(t, rt.store)
			var want []string
			for range 3 {
				want = append(want, tc.want...)
			}
			if strings.Join(types, ",") != strings.Join(want, ",") {
				t.Fatalf("events = %v, want %v", types, want)
			}
		})
	}
}

// A speaker-edit attempt copies the published meeting, summary included. The
// copy's record of that summary is not a new one unless the edit rewrote it.
func TestSpeakerEditThatKeepsTheSummaryEmitsNoSummaryEvent(t *testing.T) {
	for name, tc := range map[string]struct {
		rewrote bool
		// earlierFailed: the copied bundle still records an earlier attempt's
		// failed summary step, which says nothing about this attempt.
		earlierFailed bool
	}{
		"kept":                              {},
		"rewritten":                         {rewrote: true},
		"kept after an earlier failure":     {earlierFailed: true},
		"rewritten over an earlier failure": {rewrote: true, earlierFailed: true},
	} {
		rewrote := tc.rewrote
		t.Run(name, func(t *testing.T) {
			rt, cleanup := newBareSealRuntime(t)
			defer cleanup()
			job, run := seedBuildForEventTest(t, rt, "refine-summary")
			setAttemptKindForEventTest(t, rt, job.ID, triggerKindRefine)
			attachEventWebhookForTest(t, rt, eventSummaryCompleted+","+eventSummaryFailed)
			rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
				meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
				writeCompletedMeetingForEventTest(t, meetingPath)
				if tc.earlierFailed {
					manifest := map[string]any{"processing": map[string]any{
						"meetingSummary": map[string]any{"status": "failed", "reason": "upstream 502"},
					}}
					raw, _ := json.Marshal(manifest)
					if err := os.WriteFile(filepath.Join(meetingPath, "manifest.json"), raw, 0o600); err != nil {
						return "", err
					}
				}
				if rewrote {
					if err := rt.store.SetAttemptSummaryTimes(context.Background(), task.JobID, task.AttemptNumber, nowUTCString(), nowUTCString()); err != nil {
						return "", err
					}
				}
				return meetingPath, nil
			}
			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

			types := eventOutboxTypes(t, rt.store)
			if got := containsEvent(types, eventSummaryCompleted); got != rewrote {
				t.Fatalf("summary.completed emitted = %v, want %v (events = %v)", got, rewrote, types)
			}
			if containsEvent(types, eventSummaryFailed) {
				t.Fatalf("events = %v, an earlier attempt's failed summary is not this attempt's", types)
			}
		})
	}
}

// The metadata index learns a meeting's title at publication. The events of
// its first build are emitted before that and must name the meeting anyway,
// with the stage the job is in once the build has been handed on.
func TestFirstBuildEventsCarryTheTitleAndCurrentStage(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	job, run := seedBuildForEventTest(t, rt, "first-build")
	binding, _ := json.Marshal(talkJobBinding{BackendURL: "https://cloud.example", RoomToken: "tok", RoomName: "Weekly operations sync"})
	if _, err := rt.store.db.Exec(`UPDATE jobs SET talk_binding = ? WHERE id = ?`, string(binding), job.ID); err != nil {
		t.Fatal(err)
	}
	attachEventWebhookForTest(t, rt, eventSummaryCompleted+","+eventBuildCompleted)
	rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		writeCompletedMeetingForEventTest(t, meetingPath)
		return meetingPath, nil
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

	rows, err := rt.store.db.Query(`SELECT type, payload FROM event_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var eventType, raw string
		if err := rows.Scan(&eventType, &raw); err != nil {
			t.Fatal(err)
		}
		var payload outboundEventPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		seen++
		if payload.Data["title"] != "Weekly operations sync" {
			t.Errorf("%s title = %v, want the room's name before publication", eventType, payload.Data["title"])
		}
		if payload.Data["stage"] != "seal" || payload.Data["state"] != "queued" {
			t.Errorf("%s stage/state = %v/%v, want seal/queued", eventType, payload.Data["stage"], payload.Data["state"])
		}
	}
	if seen != 2 {
		t.Fatalf("events = %d, want summary.completed and build.completed", seen)
	}
}

// build.completed, recording.published and shares.completed announce a
// recording. A summarize or speaker-edit attempt rebuilds and republishes a
// meeting that is already out, so it announces none of them; a full rerun is
// the recording again and does.
func TestOnlyRecordingAttemptsAnnounceARecording(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want []string
	}{
		{kind: triggerKindSummarize, want: []string{eventSummaryCompleted}},
		{kind: triggerKindRefine, want: nil},
		{kind: triggerKindRerun, want: []string{eventSummaryCompleted, eventBuildCompleted, eventRecordingPublished}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			rt, cleanup := newBareSealRuntime(t)
			defer cleanup()
			job, run := seedBuildForEventTest(t, rt, "announce-"+tc.kind)
			setAttemptKindForEventTest(t, rt, job.ID, tc.kind)
			attachEventWebhookForTest(t, rt, "")
			rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
				meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
				writeCompletedMeetingForEventTest(t, meetingPath)
				return meetingPath, nil
			}
			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)
			// What the publish worker sends once the meeting is delivered.
			rt.emitRecordingEvent(eventRecordingPublished, job.ID, 1, map[string]any{"published_location": "meetings/x.opus"})

			if got := eventOutboxTypes(t, rt.store); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
		})
	}
}

// With the events it could send switched off, an emission reads nothing: not
// the attempt, not summary.md.
func TestEmissionsReadNothingForAnEventNobodyAskedFor(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	if rt.eventEnabled(eventSummaryCompleted, eventRecordingFailed) {
		t.Fatal("events are enabled on a runtime with no webhook")
	}
	attachEventWebhookForTest(t, rt, eventRecordingStarted)
	if rt.eventEnabled(eventSummaryCompleted, eventRecordingFailed) || !rt.eventEnabled(eventRecordingStarted) {
		t.Fatal("eventEnabled does not follow the allow-list")
	}
	// A job that does not exist: any lookup would be the only thing to fail.
	rt.emitAttemptFailure("no-such-job", 1, "build", "boom", nil)
	rt.emitSummaryOutcomes(buildTask{JobID: "no-such-job", AttemptNumber: 1}, "/nonexistent", stepOutcomes{SummaryStatus: "completed"})
	rt.emitRecordingEvent(eventBuildCompleted, "no-such-job", 1, nil)
	if got := eventOutboxTypes(t, rt.store); len(got) != 0 {
		t.Fatalf("events = %v, want none", got)
	}
}

// A stage cut short because the operator is stopping is reported the way the
// startup pass reports one a crash cut short, so a receiver can tell an
// upgrade from a failed recording.
func TestStageFailedByShutdownIsMarkedInterrupted(t *testing.T) {
	for name, stopping := range map[string]bool{"running": false, "stopping": true} {
		t.Run(name, func(t *testing.T) {
			rt, cleanup := newBareSealRuntime(t)
			defer cleanup()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt.ctx = ctx
			job, _ := seedBuildForEventTest(t, rt, "shutdown-seal")
			attachEventWebhookForTest(t, rt, eventRecordingFailed)
			if stopping {
				cancel()
			}
			rt.failSeal(sealTask{JobID: job.ID, AttemptNumber: 1}, "", contextError("signal: killed"), nowUTCString())

			var payload string
			if err := rt.store.db.QueryRow(`SELECT payload FROM event_outbox`).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var event outboundEventPayload
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				t.Fatal(err)
			}
			if got, _ := event.Data["interrupted"].(bool); got != stopping {
				t.Fatalf("interrupted = %v, want %v (data = %v)", event.Data["interrupted"], stopping, event.Data)
			}
		})
	}
}
