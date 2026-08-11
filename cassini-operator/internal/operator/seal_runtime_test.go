package operator

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newBareSealRuntime builds a Runtime with no workers or dispatcher, so the
// test drives runSealJob synchronously and can inspect publish/queued before a
// publish worker consumes it. Same reason newBarePublishRuntime exists.
func newBareSealRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	rt := &Runtime{
		ctx:    context.Background(),
		store:  store,
		logger: log.New(ioDiscard{}, "", 0),
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		cfg: Config{
			WorkRoot: filepath.Join(tmp, "jobs"),
			SiteRoot: filepath.Join(tmp, "site"),
		},
		// Deep enough that a single seal never blocks on the hand-off.
		publishQueue: make(chan publishTask, 4),
		requeueKick:  make(chan struct{}, 1),
	}
	return rt, func() { _ = store.Close() }
}

func TestSealQueuesPublishWithTheArtifactItSealed(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()

	insertJob(t, rt.store.db, "seal-ok", "2026-06-12T10:00:00Z")
	attemptMeeting := attemptMeetingPath(rt.cfg.WorkRoot, "seal-ok", 1)
	if err := rt.store.MarkSealQueued(context.Background(), "seal-ok", canonicalMeetingPath(rt.cfg.WorkRoot, "seal-ok"), attemptMeeting, nowUTCString()); err != nil {
		t.Fatalf("MarkSealQueued() error = %v", err)
	}
	rt.sealJobFn = writeSealedOpusFixture(rt)

	rt.runSealJob(sealTask{JobID: "seal-ok", AttemptNumber: 1, ArtifactMeetingPath: attemptMeeting})

	job := mustGetJob(t, rt.store, "seal-ok")
	if job.Stage != "publish" || job.State != "queued" {
		t.Fatalf("job = %s/%s, want publish/queued", job.Stage, job.State)
	}
	sealedPath := attemptOpusPath(rt.cfg.WorkRoot, "seal-ok", 1)
	sealedDigest, err := fileSHA256(sealedPath)
	if err != nil {
		t.Fatalf("digest the sealed artifact: %v", err)
	}
	if job.ArtifactOpusSHA256 == nil || *job.ArtifactOpusSHA256 != sealedDigest {
		t.Fatalf("recorded digest = %v, want %s", job.ArtifactOpusSHA256, sealedDigest)
	}

	// The canonical `.opus` is a promotion of the sealed artifact, byte for
	// byte — not a second, independent pack of the same meeting.
	canonical := canonicalOpusPath(rt.cfg.WorkRoot, "seal-ok")
	if job.ArtifactOpusPath == nil || *job.ArtifactOpusPath != canonical {
		t.Fatalf("job artifact_opus_path = %v, want %s", job.ArtifactOpusPath, canonical)
	}
	canonicalDigest, err := fileSHA256(canonical)
	if err != nil {
		t.Fatalf("digest the canonical artifact: %v", err)
	}
	if canonicalDigest != sealedDigest {
		t.Fatalf("canonical digest %s != sealed digest %s", canonicalDigest, sealedDigest)
	}

	// The publish task carries the immutable path, not a name to resolve later.
	select {
	case task := <-rt.publishQueue:
		if task.OpusPath != sealedPath || task.OpusSHA256 != sealedDigest {
			t.Fatalf("publish task = %#v, want the sealed path and digest", task)
		}
	default:
		t.Fatal("expected a publish task to be handed off")
	}
}

// A seal that cannot produce its artifact must end the job. Nothing falls back
// to `.meeting` any more, so letting the job continue would publish something
// the pipeline never verified — or nothing at all.
func TestSealFailureEndsTheJobAndQueuesNoPublish(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()

	insertJob(t, rt.store.db, "seal-fail", "2026-06-12T10:00:00Z")
	attemptMeeting := attemptMeetingPath(rt.cfg.WorkRoot, "seal-fail", 1)
	if err := rt.store.MarkSealQueued(context.Background(), "seal-fail", canonicalMeetingPath(rt.cfg.WorkRoot, "seal-fail"), attemptMeeting, nowUTCString()); err != nil {
		t.Fatalf("MarkSealQueued() error = %v", err)
	}
	rt.cfg.CassiniBin = writeFakeCassini(t, "echo 'pack exploded' >&2\nexit 1\n")
	rt.sealJobFn = rt.executeSealCLIWithTimeout
	if err := os.MkdirAll(attemptMeeting, 0o755); err != nil {
		t.Fatalf("seed attempt meeting: %v", err)
	}

	rt.runSealJob(sealTask{JobID: "seal-fail", AttemptNumber: 1, ArtifactMeetingPath: attemptMeeting})

	job := mustGetJob(t, rt.store, "seal-fail")
	if job.Stage != "done" || job.State != "failed" {
		t.Fatalf("job = %s/%s, want done/failed", job.Stage, job.State)
	}
	if job.Error == nil || !strings.Contains(*job.Error, "cassini pack") {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
	if _, err := os.Stat(canonicalOpusPath(rt.cfg.WorkRoot, "seal-fail")); !os.IsNotExist(err) {
		t.Fatalf("a failed seal must not promote a canonical .opus, err=%v", err)
	}
	select {
	case task := <-rt.publishQueue:
		t.Fatalf("a failed seal must queue no publish, got %#v", task)
	default:
	}
	tasks, err := rt.store.ListQueuedPublishTasks(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedPublishTasks() error = %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("a failed seal must leave no durable publish backlog, got %#v", tasks)
	}
}

func TestSealTimeoutKillsHungPack(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()

	insertJob(t, rt.store.db, "seal-hang", "2026-06-12T10:00:00Z")
	attemptMeeting := attemptMeetingPath(rt.cfg.WorkRoot, "seal-hang", 1)
	if err := os.MkdirAll(attemptMeeting, 0o755); err != nil {
		t.Fatalf("seed attempt meeting: %v", err)
	}
	if err := rt.store.MarkSealQueued(context.Background(), "seal-hang", canonicalMeetingPath(rt.cfg.WorkRoot, "seal-hang"), attemptMeeting, nowUTCString()); err != nil {
		t.Fatalf("MarkSealQueued() error = %v", err)
	}
	rt.cfg.CassiniBin = writeFakeCassini(t, "if [ \"$1\" = pack ]; then sleep 30; fi\nexit 0\n")
	rt.sealJobTimeout = 200 * time.Millisecond
	rt.sealJobFn = rt.executeSealCLIWithTimeout

	start := time.Now()
	rt.runSealJob(sealTask{JobID: "seal-hang", AttemptNumber: 1, ArtifactMeetingPath: attemptMeeting})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("hung pack was not bounded: took %s", elapsed)
	}

	job := mustGetJob(t, rt.store, "seal-hang")
	if job.Stage != "done" || job.State != "failed" {
		t.Fatalf("job = %s/%s, want done/failed", job.Stage, job.State)
	}
	if job.Error == nil || !strings.Contains(*job.Error, "seal timed out after") {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
}

// A seal queued when the operator died is resumable work, not a lost job: the
// startup sweep leaves it alone and the requeue dispatcher re-delivers it.
func TestRequeueDispatcherDeliversStrandedSeals(t *testing.T) {
	rt, cleanup := newBareSealRuntime(t)
	defer cleanup()
	rt.sealQueue = make(chan sealTask, 4)

	insertJob(t, rt.store.db, "stranded-seal", "2026-06-12T10:00:00Z")
	attemptMeeting := attemptMeetingPath(rt.cfg.WorkRoot, "stranded-seal", 1)
	if err := rt.store.MarkSealQueued(context.Background(), "stranded-seal", canonicalMeetingPath(rt.cfg.WorkRoot, "stranded-seal"), attemptMeeting, nowUTCString()); err != nil {
		t.Fatalf("MarkSealQueued() error = %v", err)
	}
	if _, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), nowUTCString()); err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}

	if backlog := rt.dispatchQueuedSealTasks(map[string]struct{}{}); backlog {
		t.Fatal("expected the seal task to be delivered, not backlogged")
	}
	select {
	case task := <-rt.sealQueue:
		if task.JobID != "stranded-seal" || task.ArtifactMeetingPath != attemptMeeting {
			t.Fatalf("unexpected redelivered seal task = %#v", task)
		}
	default:
		t.Fatal("stranded seal/queued row was not re-delivered after a restart sweep")
	}
}

func TestPromoteOpusFileReplacesTheCanonicalArtifactAtomically(t *testing.T) {
	workRoot := t.TempDir()
	if err := os.MkdirAll(currentRoot(workRoot), 0o755); err != nil {
		t.Fatalf("mkdir current: %v", err)
	}
	if err := os.WriteFile(canonicalOpusPath(workRoot, "job1"), []byte("previous"), 0o644); err != nil {
		t.Fatalf("seed previous canonical opus: %v", err)
	}
	sealed := attemptOpusPath(workRoot, "job1", 2)
	if err := os.MkdirAll(filepath.Dir(sealed), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	if err := os.WriteFile(sealed, []byte("attempt-two"), 0o644); err != nil {
		t.Fatalf("write sealed opus: %v", err)
	}

	destination, err := promoteOpusFile(workRoot, sealed, "job1")
	if err != nil {
		t.Fatalf("promoteOpusFile() error = %v", err)
	}
	if destination != canonicalOpusPath(workRoot, "job1") {
		t.Fatalf("destination = %q, want %q", destination, canonicalOpusPath(workRoot, "job1"))
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read promoted opus: %v", err)
	}
	if string(body) != "attempt-two" {
		t.Fatalf("promoted opus = %q, want the newly sealed bytes", string(body))
	}
	// The sealed artifact is immutable: promoting it must not consume it.
	if _, err := os.Stat(sealed); err != nil {
		t.Fatalf("sealed artifact must survive promotion: %v", err)
	}
	// Nothing may be left in the staging root for the next promotion to trip on.
	entries, err := os.ReadDir(currentStagingRoot(workRoot))
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging root must be empty after a promotion, got %d entries", len(entries))
	}
}

func TestPromoteOpusFileRejectsAMissingSeal(t *testing.T) {
	workRoot := t.TempDir()
	if _, err := promoteOpusFile(workRoot, attemptOpusPath(workRoot, "job1", 1), "job1"); err == nil {
		t.Fatal("expected an error when the sealed artifact does not exist")
	}
	if _, err := os.Stat(canonicalOpusPath(workRoot, "job1")); !os.IsNotExist(err) {
		t.Fatalf("no canonical .opus may be created from a missing seal, err=%v", err)
	}
}
