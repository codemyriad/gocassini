package operator

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingPriorityWaitsAndPreemptsWithoutSpendingRetries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &Runtime{ctx: ctx, store: store, cfg: Config{WorkRoot: filepath.Join(t.TempDir(), "jobs"), RecordingPriority: true, RecordingIdleGrace: 150 * time.Millisecond}, logger: log.New(io.Discard, "", 0), recordSlots: make(chan struct{}, 2), buildQueue: make(chan buildTask, 2)}
	const id = "recording-priority"
	insertJob(t, store.db, id, nowUTCString())
	capture := seedReadyRunBundle(t, rt.cfg.WorkRoot, id)
	if err := store.MarkBuildQueued(ctx, id, capture, capture, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		path := attemptMeetingPath(rt.cfg.WorkRoot, id, 1)
		if err := os.MkdirAll(path, 0755); err != nil {
			return path, err
		}
		if err := os.WriteFile(filepath.Join(path, "partial"), []byte("derived"), 0600); err != nil {
			return path, err
		}
		started <- struct{}{}
		<-ctx.Done()
		return path, ctx.Err()
	}
	if !rt.reserveRecordSlot() {
		t.Fatal("reserve")
	}
	done := make(chan struct{})
	go func() {
		rt.runBuildJob(buildTask{JobID: id, AttemptNumber: 1, ArtifactRunPath: capture}, 1)
		close(done)
	}()
	select {
	case <-started:
		t.Fatal("build started during recording")
	case <-time.After(150 * time.Millisecond):
	}
	job, _ := store.GetJob(ctx, id)
	if job.State != "queued" || job.BuildStartedAt != nil {
		t.Fatalf("waiting build claimed: %+v", job)
	}
	rt.releaseRecordSlot()
	select {
	case <-started:
		t.Fatal("idle grace skipped")
	case <-time.After(60 * time.Millisecond):
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start after idle")
	}
	if !rt.reserveRecordSlot() {
		t.Fatal("new recording refused")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("build did not yield")
	}
	job, _ = store.GetJob(ctx, id)
	if job.State != "queued" || job.BuildDeferralCount != 0 || job.BuildStartedAt != nil {
		t.Fatalf("priority retry lost state: %+v", job)
	}
	attempt, err := store.GetJobAttempt(ctx, id, 1)
	if err != nil || attempt.State != "queued" || attempt.BuildDeferralCount != 0 {
		t.Fatalf("attempt not requeued: %+v %v", attempt, err)
	}
	if _, err := os.Stat(filepath.Join(capture, "recording.mkv")); err != nil {
		t.Fatal("capture lost", err)
	}
	if _, err := os.Stat(attemptMeetingPath(rt.cfg.WorkRoot, id, 1)); !os.IsNotExist(err) {
		t.Fatal("partial output retained", err)
	}
	// Close and reopen the durable store as restart recovery does.
	rt.releaseRecordSlot()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rt.store = store
	tasks, err := store.ListQueuedBuildTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].JobID != id {
		t.Fatalf("durable retry not discoverable: %v %v", tasks, err)
	}
	select {
	case retry := <-rt.buildQueue:
		if retry.DeferralCount != 0 {
			t.Fatal(retry)
		}
	case <-time.After(time.Second):
		t.Fatal("retry not delivered")
	}
	// The same attempt can restart successfully from the preserved capture.
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		path := attemptMeetingPath(rt.cfg.WorkRoot, id, 1)
		return path, writeReadyMeetingBundleFixture(path, task.ArtifactRunPath)
	}
	rt.sealQueue = make(chan sealTask, 1)
	rt.runBuildJob(tasks[0], 1)
	job, err = store.GetJob(ctx, id)
	if err != nil || job.Stage != "seal" || job.State != "queued" {
		t.Fatalf("retry did not complete build: %+v %v", job, err)
	}
}

func TestRecordingPriorityWaitIsShutdownAware(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{ctx: ctx, cfg: Config{RecordingPriority: true}, recordSlots: make(chan struct{}, 1)}
	rt.reserveRecordSlot()
	done := make(chan error, 1)
	go func() { _, _, err := rt.beginBackgroundBuild(); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}

func TestRecordingPriorityDisabledKeepsConcurrentBuilds(t *testing.T) {
	rt := &Runtime{ctx: context.Background(), recordSlots: make(chan struct{}, 1)}
	rt.reserveRecordSlot()
	ctx, release, err := rt.beginBackgroundBuild()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}

func TestAvailableMemoryCreditsOnlyReclaimableCache(t *testing.T) {
	const mib = int64(1024 * 1024)
	tests := []struct {
		name        string
		host        int
		limit, used int64
		stat        string
		want        int
	}{
		{"observed-cache-stall", 5700, 4096 * mib, 1608 * mib, "inactive_file 1579155456\nfile_dirty 0\nfile_writeback 0\nshmem 0\n", 3994},
		{"dirty-and-shared", 8000, 4096 * mib, 2000 * mib, "inactive_file 1048576000\nfile_dirty 104857600\nfile_writeback 104857600\nshmem 104857600\n", 2796},
		{"missing-stats", 8000, 4096 * mib, 1608 * mib, "", 2488},
		{"missing-dirty", 8000, 4096 * mib, 1608 * mib, "inactive_file 1579155456\n", 2488},
		{"host-cap", 1000, 4096 * mib, 1608 * mib, "inactive_file 1579155456\nfile_dirty 0\nfile_writeback 0\nshmem 0\n", 1000},
		{"over-limit", 8000, 4096 * mib, 5000 * mib, "", 0},
		{"skewed-counters", 8000, 4096 * mib, 100 * mib, "inactive_file 1579155456\nfile_dirty 0\nfile_writeback 0\nshmem 0\n", 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := availableMemoryMB(tt.host, tt.limit, tt.used, tt.stat); got != tt.want {
				t.Fatalf("got %d want %d", got, tt.want)
			}
		})
	}
}
