package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

func attachEventWebhookForTest(t *testing.T, rt *Runtime, events ...string) {
	t.Helper()
	rt.eventWebhook.Store(newTestEventWebhook(t, rt.store, "https://hooks.example/events", "secret", events...))
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
	attachEventWebhookForTest(t, rt, eventSummaryCompleted)
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
	attachEventWebhookForTest(t, rt, eventSummaryFailed, eventRecordingFailed)
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
	attachEventWebhookForTest(t, rt, eventSummaryFailed, eventRecordingFailed)
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
	attachEventWebhookForTest(t, rt, eventSummaryFailed, eventRecordingFailed)
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
			attachEventWebhookForTest(t, rt, eventSummaryFailed, eventRecordingFailed)

			rt.failSeal(sealTask{JobID: job.ID, AttemptNumber: 1}, "", contextError("ffmpeg exited 1"), nowUTCString())
			rt.emitAttemptFailure(job.ID, 1, "publish", "upload refused", false)
			// What the startup pass sends for a job a restart interrupted.
			rt.emitAttemptFailure(job.ID, 1, "build", "operator restarted before the stage finished", true)

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
			attachEventWebhookForTest(t, rt, eventSummaryCompleted, eventSummaryFailed)
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

// recording.published announces a recording. A summarize or speaker-edit
// attempt rebuilds and republishes a meeting that is already out, so it does
// not; a full rerun is the recording again and does.
func TestOnlyRecordingAttemptsAnnounceAPublication(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want []string
	}{
		{kind: triggerKindSummarize, want: []string{eventSummaryCompleted}},
		{kind: triggerKindRefine, want: nil},
		{kind: triggerKindRerun, want: []string{eventSummaryCompleted, eventRecordingPublished}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			rt, cleanup := newBareSealRuntime(t)
			defer cleanup()
			job, run := seedBuildForEventTest(t, rt, "announce-"+tc.kind)
			setAttemptKindForEventTest(t, rt, job.ID, tc.kind)
			attachEventWebhookForTest(t, rt)
			rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
				meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
				writeCompletedMeetingForEventTest(t, meetingPath)
				return meetingPath, nil
			}
			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)
			// What the publish worker sends once the meeting is delivered.
			rt.emitRecordingPublished(job.ID, 1)

			if got := eventOutboxTypes(t, rt.store); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
		})
	}
}

// A build parked for want of memory or a GPU is not a failure: its capture is
// kept and a rerun publishes it. No event says otherwise.
func TestBlockedBuildEmitsNothing(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	insertJob(t, rt.store.db, "blocked-build", nowUTCString())
	run := seedReadyRunBundle(t, rt.cfg.WorkRoot, "blocked-build")
	if err := rt.store.MarkBuildQueued(context.Background(), "blocked-build", run, run, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	attachEventWebhookForTest(t, rt)
	rt.buildJobFn = func(context.Context, buildTask) (string, error) {
		return "", &resourceUnavailableError{resource: "gpu", detail: "no CUDA runtime", permanent: true}
	}
	rt.runBuildJob(buildTask{JobID: "blocked-build", AttemptNumber: 1, ArtifactRunPath: run}, 1)

	if job := mustGetJob(t, rt.store, "blocked-build"); job.State != "blocked" {
		t.Fatalf("job state = %s, want blocked", job.State)
	}
	if got := eventOutboxTypes(t, rt.store); len(got) != 0 {
		t.Fatalf("events = %v, want none for a blocked build", got)
	}
}

// The jobs a restart cut short are reported once the receiver is known, as a
// failure that says it was an interruption.
func TestStartupReportsInterruptedJobs(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	job, _ := seedBuildForEventTest(t, rt, "interrupted-build")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET state = 'running' WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	at := nowUTCString()
	if n, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), at); err != nil || n != 1 {
		t.Fatalf("MarkIncompleteJobsInterrupted() = %d, %v; want 1", n, err)
	}
	attachEventWebhookForTest(t, rt)
	rt.emitInterruptedAtStartup(at)

	events := eventOutboxPayloads(t, rt.store)
	if len(events) != 1 || events[0].Type != eventRecordingFailed {
		t.Fatalf("events = %+v, want one recording.failed", events)
	}
	if data := events[0].Data; data["interrupted"] != true || data["phase"] != "build" {
		t.Fatalf("data = %v, want phase build and interrupted", data)
	}
}

func eventOutboxPayloads(t *testing.T, store *Store) []outboundEventPayload {
	t.Helper()
	rows, err := store.db.Query(`SELECT payload FROM event_outbox ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []outboundEventPayload
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var event outboundEventPayload
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

// The payload of each catalog event is public contract (docs/events.md): its
// envelope and the exact fields in `data`. The fixture is a meeting that has a
// Talk room, whose recording has stopped and which has not been published, so
// record_started_at and the facts the metadata index adds at publication
// (started_at, duration_seconds) are absent.
func TestEventPayloadsMatchTheDocumentedContract(t *testing.T) {
	common := []string{"job_id", "attempt_number", "title", "room_name", "record_finished_at"}
	writeFailedSummary := func(rt *Runtime) {
		rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
			meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
			if err := os.MkdirAll(meetingPath, 0o755); err != nil {
				return "", err
			}
			raw, _ := json.Marshal(map[string]any{"processing": map[string]any{
				"meetingSummary": map[string]any{"status": "failed", "reason": "upstream 502"},
			}})
			return meetingPath, os.WriteFile(filepath.Join(meetingPath, "manifest.json"), raw, 0o600)
		}
	}
	for _, tc := range []struct {
		event string
		extra []string
		emit  func(rt *Runtime, job Job, run string)
	}{
		{event: eventRecordingStarted, extra: []string{"started_at"}, emit: func(rt *Runtime, job Job, _ string) {
			rt.emitEvent(eventRecordingStarted, job.ID, 1, map[string]any{"started_at": nowUTCString()})
		}},
		{event: eventRecordingStopped, extra: []string{"stopped_at", "stop_reason"}, emit: func(rt *Runtime, job Job, _ string) {
			rt.emitEvent(eventRecordingStopped, job.ID, 1, map[string]any{"stopped_at": nowUTCString(), "stop_reason": "room-empty"})
		}},
		{event: eventRecordingPublished, emit: func(rt *Runtime, job Job, _ string) {
			rt.emitRecordingPublished(job.ID, 1)
		}},
		{event: eventRecordingFailed, extra: []string{"phase", "error"}, emit: func(rt *Runtime, job Job, _ string) {
			rt.failSeal(sealTask{JobID: job.ID, AttemptNumber: 1}, "", contextError("ffmpeg exited 1"), nowUTCString())
		}},
		{event: eventSummaryCompleted, extra: []string{"summary_markdown", "summary_model"}, emit: func(rt *Runtime, job Job, run string) {
			rt.buildJobFn = func(_ context.Context, task buildTask) (string, error) {
				meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
				writeCompletedMeetingForEventTest(t, meetingPath)
				return meetingPath, nil
			}
			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)
		}},
		{event: eventSummaryFailed, extra: []string{"phase", "error"}, emit: func(rt *Runtime, job Job, run string) {
			writeFailedSummary(rt)
			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)
		}},
	} {
		t.Run(tc.event, func(t *testing.T) {
			rt, cleanup := newBareSealRuntime(t)
			defer cleanup()
			job, run := seedBuildForEventTest(t, rt, "contract")
			binding, _ := json.Marshal(talkJobBinding{BackendURL: "https://cloud.example", RoomToken: "tok", RoomName: "Weekly operations sync"})
			if _, err := rt.store.db.Exec(`UPDATE jobs SET talk_binding = ? WHERE id = ?`, string(binding), job.ID); err != nil {
				t.Fatal(err)
			}
			attachEventWebhookForTest(t, rt, tc.event)
			tc.emit(rt, job, run)

			var raw string
			if err := rt.store.db.QueryRow(`SELECT payload FROM event_outbox`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
				t.Fatal(err)
			}
			if got, want := sortedEventKeys(envelope), "at,cassini_version,data,id,type"; got != want {
				t.Fatalf("envelope keys = %s, want %s", got, want)
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(envelope["data"], &data); err != nil {
				t.Fatal(err)
			}
			want := map[string]json.RawMessage{}
			for _, key := range append(append([]string{}, common...), tc.extra...) {
				want[key] = nil
			}
			if got := sortedEventKeys(data); got != sortedEventKeys(want) {
				t.Fatalf("data keys = %s, want %s", got, sortedEventKeys(want))
			}
			if string(data["title"]) != `"Weekly operations sync"` {
				t.Fatalf("title = %s, want the room's name before publication", data["title"])
			}
		})
	}
}

func sortedEventKeys(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
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
