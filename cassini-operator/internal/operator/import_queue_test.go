package operator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func queueTestJob(id, user string) ImportJob {
	return ImportJob{ID: id, BatchID: "b-" + user, CreatedBy: user, SourceName: id + ".opus", StagedPath: ".CassiniImports/" + id + ".opus"}
}

func TestQueuedImportsAreReadPerCaller(t *testing.T) {
	s := openImportTestStore(t)
	ctx := context.Background()
	jobs := []ImportJob{queueTestJob("a1", "alice"), queueTestJob("a2", "alice"), queueTestJob("b1", "bob")}
	cli := ImportJob{ID: "c1", BatchID: "b", CreatedBy: "carol", SourceName: "x.opus", StagedPath: "local:/tmp/x.opus"}
	if err := s.CreateImportJobs(ctx, append(jobs, cli)); err != nil {
		t.Fatal(err)
	}
	creators, err := s.queuedImportCreators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(creators)
	// Carol has only a CLI import, which the web workers never run.
	if len(creators) != 2 || creators[0] != "alice" || creators[1] != "bob" {
		t.Fatalf("creators: %v", creators)
	}
	if next, ok, err := s.nextQueuedImportFor(ctx, "alice"); err != nil || !ok || next.ID != "a1" {
		t.Fatalf("alice's next: %+v %v %v", next, ok, err)
	}
	if next, ok, err := s.nextQueuedImportFor(ctx, "bob"); err != nil || !ok || next.ID != "b1" {
		t.Fatalf("bob's next: %+v %v %v", next, ok, err)
	}
	if _, ok, err := s.nextQueuedImportFor(ctx, "carol"); err != nil || ok {
		t.Fatalf("carol's CLI import was offered to a web worker: %v %v", ok, err)
	}
}

func TestImportQueuePositionsCountOnlyTheCallersOwnWork(t *testing.T) {
	at := func(sec int) string { return time.Date(2026, 11, 19, 10, 0, sec, 0, time.UTC).Format(time.RFC3339) }
	// Newest first, as ImportJobsFor lists them.
	jobs := []ImportJob{
		{ID: "q3", State: importStateQueued, CreatedAt: at(6)},
		{ID: "q2", State: importStateQueued, CreatedAt: at(5)},
		{ID: "failed", State: importStateFailed, CreatedAt: at(4)},
		{ID: "q1", State: importStateQueued, CreatedAt: at(3)},
		{ID: "running", State: importStateValidating, CreatedAt: at(2)},
		// Left in a processing state by a failed write, with no worker on it:
		// it must not count as ahead.
		{ID: "stale", State: importStateIndexing, CreatedAt: at(1)},
		{ID: "done", State: importStateDone, CreatedAt: at(0)},
		{ID: "cli", State: importStateQueued, StagedPath: "local:/tmp/x.opus", CreatedAt: at(0)},
	}
	setImportQueuePositions(jobs, map[string]bool{"running": true})
	want := map[string]int{"q1": 1, "q2": 2, "q3": 3}
	for _, job := range jobs {
		expected, queued := want[job.ID]
		switch {
		case queued && (job.QueuePosition == nil || *job.QueuePosition != expected):
			t.Errorf("%s: position %v, want %d", job.ID, job.QueuePosition, expected)
		case !queued && job.QueuePosition != nil:
			t.Errorf("%s: unexpected position %d", job.ID, *job.QueuePosition)
		}
	}

	// With no worker on anything, the stale row is ignored and the oldest
	// queued import is next: position 0.
	idle := []ImportJob{{ID: "next", State: importStateQueued, CreatedAt: at(1)}, {ID: "stale", State: importStateIndexing, CreatedAt: at(0)}}
	setImportQueuePositions(idle, nil)
	if idle[0].QueuePosition == nil || *idle[0].QueuePosition != 0 {
		t.Fatalf("idle position: %v", idle[0].QueuePosition)
	}

	// The worker is on a job that is still queued, waiting for a slot: that
	// job is next (0), the one after it 1.
	waiting := []ImportJob{{ID: "later", State: importStateQueued, CreatedAt: at(1)}, {ID: "slot", State: importStateQueued, CreatedAt: at(0)}}
	setImportQueuePositions(waiting, map[string]bool{"slot": true})
	if p := waiting[1].QueuePosition; p == nil || *p != 0 {
		t.Fatalf("job waiting for a slot: %v", p)
	}
	if p := waiting[0].QueuePosition; p == nil || *p != 1 {
		t.Fatalf("job behind it: %v", p)
	}
}

// queueWorkerService is an import service over a real jobs database whose
// imports are run by perform, so the workers can be driven without Nextcloud.
func queueWorkerService(t *testing.T, perform func(ctx context.Context, job ImportJob) (meetingImportResult, error)) *importService {
	t.Helper()
	s := importHandlerService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.rt.ctx = ctx
	t.Cleanup(func() {
		cancel()
		s.rt.workerWG.Wait()
	})
	s.logger = log.New(testWriter{t}, "", 0)
	s.perform = perform
	return s
}

func waitForImportState(t *testing.T, s *importService, id, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, _, err := s.rt.store.ImportJob(context.Background(), id)
		if err == nil && job.State == state {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: state %q, want %q", id, job.State, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOneCallersImportDoesNotHoldAnothers(t *testing.T) {
	started := make(chan string, 8)
	release := map[string]chan struct{}{"a1": make(chan struct{}), "a2": make(chan struct{}), "b1": make(chan struct{})}
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		started <- job.ID
		select {
		case <-release[job.ID]:
		case <-ctx.Done():
			return meetingImportResult{}, ctx.Err()
		}
		// Settle as failed: a success would try to delete the staged copy.
		return meetingImportResult{}, errors.New("test import settled")
	})
	ctx := context.Background()
	if err := s.rt.store.CreateImportJobs(ctx, []ImportJob{queueTestJob("a1", "alice"), queueTestJob("a2", "alice"), queueTestJob("b1", "bob")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()

	// Alice's first import and Bob's import both start, although Alice's
	// queued first and is still running.
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case id := <-started:
			got[id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("started only %v", got)
		}
	}
	if !got["a1"] || !got["b1"] {
		t.Fatalf("started %v, want a1 and b1", got)
	}
	// A second dispatch does not give Alice a second worker.
	s.dispatch()
	select {
	case id := <-started:
		t.Fatalf("%s started while alice's a1 was running", id)
	case <-time.After(100 * time.Millisecond):
	}

	// Bob's settles while Alice's is still running.
	close(release["b1"])
	waitForImportState(t, s, "b1", importStateFailed)

	// Alice's second import starts only once her first has settled.
	close(release["a1"])
	select {
	case id := <-started:
		if id != "a2" {
			t.Fatalf("started %s, want a2", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a2 never started")
	}
	waitForImportState(t, s, "a1", importStateFailed)

	// Cancel reaches the running job through the map of running jobs.
	if w := importDo(t, s, http.MethodPost, importsURLPath+"/a2/cancel", "alice", ""); w.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.isRunning("a2") {
		if time.Now().After(deadline) {
			t.Fatal("a2 still running after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitForImportState(t, s, "a2", importStateCanceled)
}

func TestImportSlotsCapImportsAcrossCallers(t *testing.T) {
	var current, peak atomic.Int32
	started := make(chan string, 16)
	release := make(chan struct{})
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		n := current.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer current.Add(-1)
		started <- job.ID
		select {
		case <-release:
		case <-ctx.Done():
			return meetingImportResult{}, ctx.Err()
		}
		return meetingImportResult{}, errors.New("test import settled")
	})
	s.importSlots = make(chan struct{}, 3)
	const users = 10
	var jobs []ImportJob
	for i := 0; i < users; i++ {
		user := fmt.Sprintf("user%02d", i)
		jobs = append(jobs, queueTestJob("j-"+user, user))
	}
	if err := s.rt.store.CreateImportJobs(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	s.dispatch()

	// Exactly three run; the other seven wait, still queued.
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d imports started", i)
		}
	}
	select {
	case id := <-started:
		t.Fatalf("%s started beyond the cap", id)
	case <-time.After(150 * time.Millisecond):
	}
	queued := 0
	for _, job := range jobs {
		if got, _, _ := s.rt.store.ImportJob(context.Background(), job.ID); got.State == importStateQueued {
			queued++
		}
	}
	if queued != users-3 {
		t.Fatalf("%d imports still queued, want %d", queued, users-3)
	}

	// Releasing them lets every import run, never more than three at once.
	close(release)
	for _, job := range jobs {
		waitForImportState(t, s, job.ID, importStateFailed)
	}
	if p := peak.Load(); p != 3 {
		t.Fatalf("peak concurrent imports %d, want 3", p)
	}
}

func TestImportSlotWaitEndsWithTheImport(t *testing.T) {
	s := &importService{importSlots: make(chan struct{}, 1)}
	ctx := context.Background()
	held, err := s.acquireImportSlot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A second waits, and gives up when its import is canceled.
	waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := s.acquireImportSlot(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second import: %v", err)
	}
	held()
	next, err := s.acquireImportSlot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestStalledStagedDownloadIsAbandoned(t *testing.T) {
	idle, poll := importFetchIdleTimeout, importFetchIdlePoll
	importFetchIdleTimeout, importFetchIdlePoll = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { importFetchIdleTimeout, importFetchIdlePoll = idle, poll })

	var stall atomic.Bool
	stall.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OggS partial"))
		w.(http.Flusher).Flush()
		if stall.Load() {
			// Headers and some bytes, then nothing until the client gives up.
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	s := &importService{
		exapp:  ExAppConfig{NextcloudURL: srv.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"},
		client: srv.Client(),
		logger: log.New(testWriter{t}, "", 0),
	}
	job := queueTestJob("stalled", "alice")
	local := filepath.Join(t.TempDir(), "staged")

	begun := time.Now()
	err := s.fetchStaged(context.Background(), job, local)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("stalled download: %v", err)
	}
	if elapsed := time.Since(begun); elapsed > 5*time.Second {
		t.Fatalf("gave up after %s", elapsed)
	}

	// A download that keeps flowing is not cut off.
	stall.Store(false)
	if err := s.fetchStaged(context.Background(), job, local); err != nil {
		t.Fatalf("complete download: %v", err)
	}
	if got, _ := os.ReadFile(local); string(got) != "OggS partial" {
		t.Fatalf("downloaded %q", got)
	}
}
