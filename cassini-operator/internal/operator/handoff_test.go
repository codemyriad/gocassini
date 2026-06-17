package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedReadyRunBundle writes a finalized (state=ready) run bundle for jobID's
// first attempt and returns its path.
func seedReadyRunBundle(t *testing.T, workRoot, jobID string) string {
	t.Helper()
	runPath := attemptRunPath(workRoot, jobID, 1)
	bundle, err := PrepareRunBundle(runPath, false)
	if err != nil {
		t.Fatalf("PrepareRunBundle(%s) error = %v", jobID, err)
	}
	if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write recording for %s: %v", jobID, err)
	}
	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: "Test"}); err != nil {
		t.Fatalf("FinalizeRunBundle(%s) error = %v", jobID, err)
	}
	return bundle.RootDir
}

func setJobArtifactRunPath(t *testing.T, db *sql.DB, id, runPath string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE jobs SET artifact_run_path = ? WHERE id = ?`, runPath, id); err != nil {
		t.Fatalf("set artifact_run_path for %s: %v", id, err)
	}
}

// blockingBuildFixture replaces buildJobFn with one that signals each started
// build and waits for release before producing a ready meeting bundle.
func blockingBuildFixture(rt *Runtime, started chan<- string, release <-chan struct{}) {
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		started <- task.JobID
		<-release
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}
}

// occupyBuildWorkerAndFillQueue parks the single build worker on a claimable
// blocker job and stuffs the channel with unclaimable filler tasks, so the
// next channel send would block forever without non-blocking handoffs.
func occupyBuildWorkerAndFillQueue(t *testing.T, rt *Runtime, started <-chan string) {
	t.Helper()
	blockerRun := seedReadyRunBundle(t, rt.cfg.WorkRoot, "blocker")
	insertJob(t, rt.store.db, "blocker", "2026-06-12T10:00:00Z")
	if err := rt.enqueueBuildJob("blocker", 1, blockerRun, blockerRun, nowUTCString()); err != nil {
		t.Fatalf("enqueueBuildJob(blocker) error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("build worker did not pick up the blocker job")
	}
	// Fillers have no DB row, so workers fail the claim and skip them.
	for i := 0; i < cap(rt.buildQueue); i++ {
		rt.buildQueue <- buildTask{JobID: fmt.Sprintf("filler-%d", i), AttemptNumber: 1}
	}
}

func TestEnqueueBuildJobDoesNotBlockWhenQueueFullAndRequeues(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	release := make(chan struct{})
	started := make(chan string, 64)
	blockingBuildFixture(rt, started, release)
	occupyBuildWorkerAndFillQueue(t, rt, started)

	overflowRun := seedReadyRunBundle(t, rt.cfg.WorkRoot, "overflow")
	insertJob(t, rt.store.db, "overflow", "2026-06-12T10:01:00Z")
	enqueued := make(chan error, 1)
	go func() {
		enqueued <- rt.enqueueBuildJob("overflow", 1, overflowRun, overflowRun, nowUTCString())
	}()
	select {
	case err := <-enqueued:
		if err != nil {
			t.Fatalf("enqueueBuildJob(overflow) error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("enqueueBuildJob blocked on a full build queue")
	}

	job := mustGetJob(t, rt.store, "overflow")
	if job.Stage != "build" || job.State != "queued" {
		t.Fatalf("overflow job should be durably queued, got stage=%s state=%s", job.Stage, job.State)
	}

	// Once the worker frees up, the requeue dispatcher must deliver the
	// dropped task without any further intervention.
	close(release)
	waitForJobState(t, rt.store, "blocker", "succeeded")
	waitForJobState(t, rt.store, "overflow", "succeeded")
}

func TestRerunHandlerDoesNotBlockWhenBuildQueueFull(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	release := make(chan struct{})
	started := make(chan string, 64)
	blockingBuildFixture(rt, started, release)
	occupyBuildWorkerAndFillQueue(t, rt, started)

	rerunRun := seedReadyRunBundle(t, rt.cfg.WorkRoot, "rerun-me")
	seedJobRow(t, rt.store.db, seededJobRow{ID: "rerun-me", Stage: "done", State: "failed", CreatedAt: "2026-06-12T09:00:00Z", CompletedAt: strPtr("2026-06-12T09:30:00Z")})
	setJobArtifactRunPath(t, rt.store.db, "rerun-me", rerunRun)

	responded := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/jobs/rerun-me/rerun", nil)
		rec := httptest.NewRecorder()
		rt.jobDetailHandler(rec, req)
		responded <- rec
	}()
	select {
	case rec := <-responded:
		if rec.Code != http.StatusAccepted {
			t.Fatalf("rerun status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rerun handler blocked the HTTP request on a full build queue")
	}

	close(release)
	job := waitForJobState(t, rt.store, "rerun-me", "succeeded")
	if job.CurrentAttemptNumber != 2 {
		t.Fatalf("current_attempt_number = %d, want 2", job.CurrentAttemptNumber)
	}
}

func TestRequeueDispatcherDeliversQueuedRowsAfterRestartSweep(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	// Rows that exist only in the DB — as after an operator restart — must
	// survive the startup sweep as queued and be delivered by the dispatcher
	// scan, not stranded behind a manual rerun.
	runPath := seedReadyRunBundle(t, rt.cfg.WorkRoot, "stranded-build")
	seedJobRow(t, rt.store.db, seededJobRow{ID: "stranded-build", Stage: "build", State: "queued", CreatedAt: "2026-06-12T10:00:00Z"})
	setJobArtifactRunPath(t, rt.store.db, "stranded-build", runPath)
	seedJobRow(t, rt.store.db, seededJobRow{ID: "stranded-publish", Stage: "publish", State: "queued", CreatedAt: "2026-06-12T10:01:00Z"})

	interrupted, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), nowUTCString())
	if err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if interrupted != 0 {
		t.Fatalf("interrupted = %d, want 0 (queued build/publish rows must stay queued)", interrupted)
	}

	rt.kickRequeueScan()
	waitForJobState(t, rt.store, "stranded-build", "succeeded")
	waitForJobState(t, rt.store, "stranded-publish", "succeeded")
}

func TestRecordSlotFreedBeforeTalkDeliveryCompletes(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(deliveryStarted) })
		<-releaseDelivery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	var releasedOnce sync.Once
	releaseFn := func() { releasedOnce.Do(func() { close(releaseDelivery) }) }
	defer releaseFn()

	state := &talkRoomState{
		RoomKey:    "talk.example|tok1",
		BackendURL: server.URL,
		RoomToken:  "tok1",
		Owner:      "admin",
		StartActor: &talkActorData{Type: "users", ID: "admin"},
	}
	if !rt.reserveTalkRoom(state) {
		t.Fatal("reserveTalkRoom failed")
	}

	req := TriggerRequest{
		Platform:              nextcloudTalkProvider,
		URL:                   "https://example.test/call",
		GuestName:             defaultGuestName,
		StopWhenRoomEmpty:     true,
		RoomEmptyGraceSeconds: defaultRoomEmptySec,
	}
	body, err := encodeTriggerRequest(req)
	if err != nil {
		t.Fatalf("encodeTriggerRequest() error = %v", err)
	}
	resp, startRecord, err := rt.prepareRecordJob(context.Background(), nextcloudTalkProvider, body, req)
	if err != nil {
		t.Fatalf("prepareRecordJob() error = %v", err)
	}
	rt.bindTalkRoomJob(state, resp.ID)
	startRecord()

	select {
	case <-deliveryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("talk delivery never started")
	}

	// The record subprocess has exited (delivery only runs afterwards), so
	// the record slot must already be free even though delivery is still in
	// flight (D-367). Before the fix the slot was held until the deferred
	// release at goroutine exit, after delivery and the build handoff.
	select {
	case rt.recordSlots <- struct{}{}:
		<-rt.recordSlots
	case <-time.After(2 * time.Second):
		t.Fatal("record slot still held during talk delivery")
	}

	releaseFn()
	waitForJobState(t, rt.store, resp.ID, "succeeded")
}

func TestPublishTimeoutKillsHungPublish(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	script := filepath.Join(tmp, "hang-cassini.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = publish ]; then sleep 30; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hang script: %v", err)
	}

	// A bare runtime (no workers/dispatcher) so the hung publish is driven
	// synchronously by the test.
	rt := &Runtime{
		ctx:    context.Background(),
		store:  store,
		logger: log.New(ioDiscard{}, "", 0),
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		cfg: Config{
			WorkRoot:   filepath.Join(tmp, "jobs"),
			SiteRoot:   filepath.Join(tmp, "site"),
			CassiniBin: script,
		},
		publishJobTimeout: 200 * time.Millisecond,
	}
	rt.publishJobFn = rt.executePublishCLIWithTimeout

	insertJob(t, store.db, "pub-timeout", "2026-06-12T10:00:00Z")
	if err := store.MarkPublishQueued(context.Background(), "pub-timeout", "/tmp/meeting", "/tmp/meeting", nowUTCString()); err != nil {
		t.Fatalf("MarkPublishQueued() error = %v", err)
	}

	start := time.Now()
	rt.runPublishJob(publishTask{JobID: "pub-timeout", AttemptNumber: 1})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("hung publish was not bounded: took %s", elapsed)
	}

	job := mustGetJob(t, store, "pub-timeout")
	if job.Stage != "done" || job.State != "failed" {
		t.Fatalf("job = %s/%s, want done/failed", job.Stage, job.State)
	}
	if job.Error == nil || !strings.Contains(*job.Error, "publish timed out after") {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
}

func TestClaimBuildRunningClaimsOnlyQueuedRows(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	seedJobRow(t, store.db, seededJobRow{ID: "claim-me", Stage: "build", State: "queued", CreatedAt: "2026-06-12T10:00:00Z"})

	claimed, err := store.ClaimBuildRunning(context.Background(), "claim-me", nowUTCString())
	if err != nil {
		t.Fatalf("first ClaimBuildRunning() error = %v", err)
	}
	if !claimed {
		t.Fatal("first claim should succeed")
	}
	claimed, err = store.ClaimBuildRunning(context.Background(), "claim-me", nowUTCString())
	if err != nil {
		t.Fatalf("second ClaimBuildRunning() error = %v", err)
	}
	if claimed {
		t.Fatal("duplicate claim should be skipped, not re-run")
	}
}

func TestMarkPublishRunningClaimsOnlyQueuedRows(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	seedJobRow(t, store.db, seededJobRow{ID: "pub-claim", Stage: "publish", State: "queued", CreatedAt: "2026-06-12T10:00:00Z"})

	if err := store.MarkPublishRunning(context.Background(), "pub-claim", nowUTCString()); err != nil {
		t.Fatalf("first MarkPublishRunning() error = %v", err)
	}
	err = store.MarkPublishRunning(context.Background(), "pub-claim", nowUTCString())
	if !errors.Is(err, errPublishNotClaimable) {
		t.Fatalf("duplicate claim error = %v, want errPublishNotClaimable", err)
	}
}

func TestEventHubClosesOverflowedSubscriber(t *testing.T) {
	hub := newEventHub()
	subscriber, unsubscribe := hub.Subscribe()

	for i := 0; i < cap(subscriber)+1; i++ {
		hub.Publish(StateChangeEvent{Type: "job.updated", JobID: fmt.Sprintf("job-%d", i), At: nowUTCString()})
	}

	received := 0
	for {
		select {
		case _, ok := <-subscriber:
			if !ok {
				if received != cap(subscriber) {
					t.Fatalf("received = %d events before close, want %d", received, cap(subscriber))
				}
				// Unsubscribing after the hub closed the channel must not
				// double-close.
				unsubscribe()
				return
			}
			received++
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber channel was not closed on overflow; received %d events", received)
		}
	}
}
