package operator

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func newSealTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// setAttemptMeetingPath stands in for what the build worker records, so a seal
// task can be listed the way the requeue dispatcher lists it.
func setAttemptMeetingPath(t *testing.T, db *sql.DB, jobID string, attemptNumber int, path string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE job_attempts SET artifact_meeting_path = ? WHERE job_id = ? AND attempt_number = ?`, path, jobID, attemptNumber); err != nil {
		t.Fatalf("set attempt meeting path: %v", err)
	}
}

func TestMarkSealQueuedMovesTheJobAndItsAttemptToSeal(t *testing.T) {
	store := newSealTestStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "job1", Stage: "build", State: "running", CreatedAt: "2026-06-12T00:00:00Z"})

	if err := store.MarkSealQueued(context.Background(), "job1", "/work/current/job1.meeting", "/work/runs/job1--attempt-001.meeting", "2026-06-12T00:01:00Z"); err != nil {
		t.Fatalf("MarkSealQueued() error = %v", err)
	}

	job := mustGetJob(t, store, "job1")
	if job.Stage != "seal" || job.State != "queued" {
		t.Fatalf("job stage/state = %s/%s, want seal/queued", job.Stage, job.State)
	}
	if job.SealQueuedAt == nil || *job.SealQueuedAt != "2026-06-12T00:01:00Z" {
		t.Fatalf("seal_queued_at = %v, want the queue timestamp", job.SealQueuedAt)
	}
	if job.ArtifactMeetingPath == nil || *job.ArtifactMeetingPath != "/work/current/job1.meeting" {
		t.Fatalf("job artifact_meeting_path = %v, want the canonical bundle", job.ArtifactMeetingPath)
	}
	attempts, err := store.ListJobAttempts(context.Background(), "job1")
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].Stage != "seal" || attempts[0].State != "queued" {
		t.Fatalf("unexpected attempt after MarkSealQueued = %#v", attempts)
	}
	// The attempt row carries the attempt-local bundle; the jobs row carries the
	// canonical one. Sealing packs the attempt's own bundle.
	if attempts[0].ArtifactMeetingPath == nil || *attempts[0].ArtifactMeetingPath != "/work/runs/job1--attempt-001.meeting" {
		t.Fatalf("attempt artifact_meeting_path = %v, want the attempt bundle", attempts[0].ArtifactMeetingPath)
	}
}

func TestClaimSealRunningOnlyClaimsQueuedSealRows(t *testing.T) {
	store := newSealTestStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "job1", Stage: "seal", State: "queued", CreatedAt: "2026-06-12T00:00:00Z"})

	claimed, err := store.ClaimSealRunning(context.Background(), "job1", "2026-06-12T00:02:00Z")
	if err != nil {
		t.Fatalf("ClaimSealRunning() error = %v", err)
	}
	if !claimed {
		t.Fatal("expected the first claim to win")
	}

	// A duplicate delivery (direct enqueue plus a requeue-dispatcher re-scan)
	// must be skipped, never run twice.
	claimedAgain, err := store.ClaimSealRunning(context.Background(), "job1", "2026-06-12T00:03:00Z")
	if err != nil {
		t.Fatalf("ClaimSealRunning() second error = %v", err)
	}
	if claimedAgain {
		t.Fatal("expected the second claim to be refused")
	}

	job := mustGetJob(t, store, "job1")
	if job.Stage != "seal" || job.State != "running" {
		t.Fatalf("job stage/state = %s/%s, want seal/running", job.Stage, job.State)
	}
	if job.SealStartedAt == nil || *job.SealStartedAt != "2026-06-12T00:02:00Z" {
		t.Fatalf("seal_started_at = %v, want the first claim's timestamp", job.SealStartedAt)
	}
}

func TestMarkSealSucceededQueuesPublishWithTheSealedArtifact(t *testing.T) {
	store := newSealTestStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "job1", Stage: "seal", State: "running", CreatedAt: "2026-06-12T00:00:00Z"})

	if err := store.MarkSealSucceeded(context.Background(), "job1",
		"/work/current/job1.opus",
		"/work/runs/job1--attempt-001.opus",
		"dead00beef",
		"2026-06-12T00:04:00Z",
	); err != nil {
		t.Fatalf("MarkSealSucceeded() error = %v", err)
	}

	job := mustGetJob(t, store, "job1")
	if job.Stage != "publish" || job.State != "queued" {
		t.Fatalf("job stage/state = %s/%s, want publish/queued", job.Stage, job.State)
	}
	if job.ArtifactOpusPath == nil || *job.ArtifactOpusPath != "/work/current/job1.opus" {
		t.Fatalf("job artifact_opus_path = %v, want the canonical promotion", job.ArtifactOpusPath)
	}
	if job.ArtifactOpusSHA256 == nil || *job.ArtifactOpusSHA256 != "dead00beef" {
		t.Fatalf("job artifact_opus_sha256 = %v, want the sealed digest", job.ArtifactOpusSHA256)
	}
	attempts, err := store.ListJobAttempts(context.Background(), "job1")
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	// The attempt row is where the immutable path lives — it is what the publish
	// worker delivers, and what a restart rebuilds the publish task from.
	if attempts[0].ArtifactOpusPath == nil || *attempts[0].ArtifactOpusPath != "/work/runs/job1--attempt-001.opus" {
		t.Fatalf("attempt artifact_opus_path = %v, want the attempt-scoped seal", attempts[0].ArtifactOpusPath)
	}
	if attempts[0].ArtifactOpusSHA256 == nil || *attempts[0].ArtifactOpusSHA256 != "dead00beef" {
		t.Fatalf("attempt artifact_opus_sha256 = %v", attempts[0].ArtifactOpusSHA256)
	}
}

func TestMarkSealFailedEndsTheJob(t *testing.T) {
	store := newSealTestStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "job1", Stage: "seal", State: "running", CreatedAt: "2026-06-12T00:00:00Z"})

	if err := store.MarkSealFailed(context.Background(), "job1", "/work/runs/job1--attempt-001.opus", "cassini pack: exit status 1", "2026-06-12T00:04:00Z"); err != nil {
		t.Fatalf("MarkSealFailed() error = %v", err)
	}

	job := mustGetJob(t, store, "job1")
	if job.Stage != "done" || job.State != "failed" {
		t.Fatalf("job stage/state = %s/%s, want done/failed", job.Stage, job.State)
	}
	if job.Error == nil || *job.Error != "cassini pack: exit status 1" {
		t.Fatalf("job error = %v, want the pack failure", job.Error)
	}
	// A failed seal must never leave a publishable row behind.
	tasks, err := store.ListQueuedPublishTasks(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedPublishTasks() error = %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no queued publish after a failed seal, got %#v", tasks)
	}
}

func TestListQueuedSealTasksCarriesTheAttemptBundle(t *testing.T) {
	store := newSealTestStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "job1", Stage: "seal", State: "queued", CreatedAt: "2026-06-12T00:00:00Z"})
	seedJobRow(t, store.db, seededJobRow{ID: "job2", Stage: "build", State: "queued", CreatedAt: "2026-06-12T00:00:01Z"})

	// A seal row with no meeting bundle recorded has nothing to pack and must
	// not be dispatched.
	tasks, err := store.ListQueuedSealTasks(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedSealTasks() error = %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no seal tasks without a meeting path, got %#v", tasks)
	}

	setAttemptMeetingPath(t, store.db, "job1", 1, "/work/runs/job1--attempt-001.meeting")
	tasks, err = store.ListQueuedSealTasks(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedSealTasks() error = %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected exactly the seal/queued job, got %#v", tasks)
	}
	if tasks[0].JobID != "job1" || tasks[0].AttemptNumber != 1 || tasks[0].ArtifactMeetingPath != "/work/runs/job1--attempt-001.meeting" {
		t.Fatalf("unexpected seal task = %#v", tasks[0])
	}
}

// A seal that was queued when the operator died is resumable: its input is a
// durable bundle on disk and the requeue dispatcher re-delivers it. Only a seal
// that was *running* lost a subprocess and is genuinely interrupted.
func TestStartupSweepLeavesQueuedSealsAloneAndInterruptsRunningOnes(t *testing.T) {
	store := newSealTestStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "queued", Stage: "seal", State: "queued", CreatedAt: "2026-06-12T00:00:00Z"})
	seedJobRow(t, store.db, seededJobRow{ID: "running", Stage: "seal", State: "running", CreatedAt: "2026-06-12T00:00:01Z"})

	if _, err := store.MarkIncompleteJobsInterrupted(context.Background(), "2026-06-12T01:00:00Z"); err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}

	queued := mustGetJob(t, store, "queued")
	if queued.Stage != "seal" || queued.State != "queued" {
		t.Fatalf("seal/queued must survive the sweep, got %s/%s", queued.Stage, queued.State)
	}
	running := mustGetJob(t, store, "running")
	if running.Stage != "seal" || running.State != "interrupted" {
		t.Fatalf("seal/running must be interrupted, got %s/%s", running.Stage, running.State)
	}
}

// The upgrade path: a row queued for publish by the previous build has no sealed
// `.opus`, and the publish worker now refuses to run without one. The migration
// sends it back to seal rather than stranding it.
func TestSealMigrationReroutesInFlightPublishRows(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	seedJobRow(t, store.db, seededJobRow{ID: "inflight", Stage: "publish", State: "queued", CreatedAt: "2026-06-12T00:00:00Z"})
	seedJobRow(t, store.db, seededJobRow{ID: "running", Stage: "publish", State: "running", CreatedAt: "2026-06-12T00:00:01Z"})
	seedJobRow(t, store.db, seededJobRow{ID: "done", Stage: "done", State: "succeeded", CreatedAt: "2026-06-12T00:00:02Z"})

	// Roll back past the seal migration and forward again: the up-migration's
	// re-route is what an upgrading deployment runs.
	if err := store.migrateDownTo(4); err != nil {
		t.Fatalf("migrateDownTo(4) error = %v", err)
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}

	inflight := mustGetJob(t, store, "inflight")
	if inflight.Stage != "seal" || inflight.State != "queued" {
		t.Fatalf("in-flight publish/queued = %s/%s, want seal/queued", inflight.Stage, inflight.State)
	}
	if inflight.SealQueuedAt == nil {
		t.Fatal("re-routed row must carry seal_queued_at so the dispatcher can order it")
	}
	attempts, err := store.ListJobAttempts(context.Background(), "inflight")
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if attempts[0].Stage != "seal" || attempts[0].State != "queued" {
		t.Fatalf("attempt row must be re-routed too, got %s/%s", attempts[0].Stage, attempts[0].State)
	}

	// A publish already running is left alone; the startup sweep owns it.
	if running := mustGetJob(t, store, "running"); running.Stage != "publish" || running.State != "running" {
		t.Fatalf("publish/running = %s/%s, want it untouched", running.Stage, running.State)
	}
	if done := mustGetJob(t, store, "done"); done.Stage != "done" || done.State != "succeeded" {
		t.Fatalf("terminal job = %s/%s, want it untouched", done.Stage, done.State)
	}
	_ = store.Close()
}
