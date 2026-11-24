package operator

import (
	"context"
	"encoding/json"
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
	s.rt.ctx, s.rt.cancel = ctx, cancel
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

// stagedFileServer serves a staged file the way the test's mode says.
func stagedFileServer(t *testing.T, mode *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		write := func(b []byte) bool {
			if _, err := w.Write(b); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		switch mode.Load().(string) {
		case "stall":
			// Headers and some bytes, then nothing until the client gives up.
			write([]byte("OggS partial"))
			<-r.Context().Done()
		case "trickle":
			// One byte at a time, forever: data, but no real progress.
			for write([]byte("x")) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		case "slow":
			// Steady, slower than the idle window overall, faster than the
			// minimum progress within it.
			for i := 0; i < 15; i++ {
				if !write(make([]byte, 2048)) {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStagedDownloadMustMakeProgress(t *testing.T) {
	idle, minProgress, poll := importFetchIdleTimeout, importFetchMinProgress, importFetchIdlePoll
	importFetchIdleTimeout, importFetchMinProgress, importFetchIdlePoll = 300*time.Millisecond, 1024, 20*time.Millisecond
	t.Cleanup(func() { importFetchIdleTimeout, importFetchMinProgress, importFetchIdlePoll = idle, minProgress, poll })

	var mode atomic.Value
	srv := stagedFileServer(t, &mode)
	s := &importService{
		exapp:  ExAppConfig{NextcloudURL: srv.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"},
		client: srv.Client(),
		logger: log.New(testWriter{t}, "", 0),
	}
	job := queueTestJob("download", "alice")

	for _, stalled := range []string{"stall", "trickle"} {
		mode.Store(stalled)
		begun := time.Now()
		err := s.fetchStaged(context.Background(), job, filepath.Join(t.TempDir(), "staged"))
		if err == nil || !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("%s: %v", stalled, err)
		}
		if elapsed := time.Since(begun); elapsed > 5*time.Second {
			t.Fatalf("%s: gave up after %s", stalled, elapsed)
		}
	}

	// A download that keeps making progress is not cut off, even though it
	// takes longer than the idle window.
	mode.Store("slow")
	local := filepath.Join(t.TempDir(), "staged")
	begun := time.Now()
	if err := s.fetchStaged(context.Background(), job, local); err != nil {
		t.Fatalf("slow download: %v", err)
	}
	if elapsed := time.Since(begun); elapsed < importFetchIdleTimeout {
		t.Fatalf("slow download took only %s; the test proves nothing", elapsed)
	}
	if info, err := os.Stat(local); err != nil || info.Size() != 15*2048 {
		t.Fatalf("downloaded %v %v", info, err)
	}
}

func TestImportOverItsTimeLimitFails(t *testing.T) {
	limit := importTimeLimit
	importTimeLimit = func(ImportJob) time.Duration { return 100 * time.Millisecond }
	t.Cleanup(func() { importTimeLimit = limit })
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		// A step that never finishes on its own.
		<-ctx.Done()
		return meetingImportResult{}, ctx.Err()
	})
	s.importSlots = make(chan struct{}, 1)
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{queueTestJob("slow", "alice"), queueTestJob("next", "bob")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	// Both fail on their own limit, one after the other through the single
	// slot: the slot is given back.
	waitForImportState(t, s, "slow", importStateFailed)
	waitForImportState(t, s, "next", importStateFailed)
	job, _, _ := s.rt.store.ImportJob(context.Background(), "slow")
	if !strings.Contains(job.Error, "did not finish within") {
		t.Fatalf("error: %q", job.Error)
	}
}

func TestWaitingForASlotCanBeCanceledAndIsPositioned(t *testing.T) {
	release := make(chan struct{})
	ran := make(chan string, 8)
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		ran <- job.ID
		select {
		case <-release:
		case <-ctx.Done():
			return meetingImportResult{}, ctx.Err()
		}
		return meetingImportResult{}, errors.New("test import settled")
	})
	s.importSlots = make(chan struct{}, 1)
	ctx := context.Background()
	if err := s.rt.store.CreateImportJobs(ctx, []ImportJob{queueTestJob("a1", "alice")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	if id := <-ran; id != "a1" {
		t.Fatalf("ran %s", id)
	}
	// Bob's two imports: the first waits for the slot Alice holds.
	if err := s.rt.store.CreateImportJobs(ctx, []ImportJob{queueTestJob("b1", "bob"), queueTestJob("b2", "bob")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	deadline := time.Now().Add(5 * time.Second)
	for !s.isRunning("b1") {
		if time.Now().After(deadline) {
			t.Fatal("bob's worker never took b1")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The real listing says b1 is next and b2 behind it.
	w := importDo(t, s, http.MethodGet, importsURLPath, "bob", "")
	var listed struct {
		Imports []ImportJob `json:"imports"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for _, job := range listed.Imports {
		if job.QueuePosition != nil {
			positions[job.ID] = *job.QueuePosition
		}
	}
	if len(positions) != 2 || positions["b1"] != 0 || positions["b2"] != 1 {
		t.Fatalf("positions: %v", positions)
	}
	// While b1 waits, it cannot be retried or removed out from under its worker.
	if w := importDo(t, s, http.MethodPost, importsURLPath+"/b1/remove", "bob", ""); w.Code != http.StatusConflict {
		t.Fatalf("remove while waiting: %d %s", w.Code, w.Body.String())
	}

	// Cancel b1 while it waits: it never runs, and b2 takes its place.
	if w := importDo(t, s, http.MethodPost, importsURLPath+"/b1/cancel", "bob", ""); w.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	waitForImportState(t, s, "b1", importStateCanceled)
	close(release)
	if id := <-ran; id != "b2" {
		t.Fatalf("ran %s after the cancel, want b2", id)
	}
	waitForImportState(t, s, "b2", importStateFailed)
	select {
	case id := <-ran:
		t.Fatalf("%s ran", id)
	default:
	}
}

func TestShutdownReturnsRunningAndWaitingImportsToTheQueue(t *testing.T) {
	ran := make(chan string, 4)
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		ran <- job.ID
		<-ctx.Done()
		return meetingImportResult{}, ctx.Err()
	})
	s.importSlots = make(chan struct{}, 1)
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{queueTestJob("a1", "alice"), queueTestJob("b1", "bob")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	<-ran
	deadline := time.Now().Add(5 * time.Second)
	for !s.isRunning("a1") || !s.isRunning("b1") {
		if time.Now().After(deadline) {
			t.Fatal("workers never took both jobs")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stop the operator: one import was running, the other waiting.
	s.rt.cancel()
	s.rt.workerWG.Wait()
	for _, id := range []string{"a1", "b1"} {
		if job, _, _ := s.rt.store.ImportJob(context.Background(), id); job.State != importStateQueued {
			t.Fatalf("%s after shutdown: %s", id, job.State)
		}
	}
}

func TestAClaimThatCannotBeWrittenIsRetriedLater(t *testing.T) {
	retryMin, retryMax := importClaimRetryMin, importClaimRetryMax
	importClaimRetryMin, importClaimRetryMax = 20*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { importClaimRetryMin, importClaimRetryMax = retryMin, retryMax })
	ran := make(chan string, 4)
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		ran <- job.ID
		return meetingImportResult{}, errors.New("test import settled")
	})
	db := s.rt.store.db
	// The database refuses every claim until the trigger is dropped.
	if _, err := db.Exec(`CREATE TRIGGER refuse_claim BEFORE UPDATE OF state ON import_jobs WHEN NEW.state='fetching' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{queueTestJob("a1", "alice")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	time.Sleep(200 * time.Millisecond)
	select {
	case id := <-ran:
		t.Fatalf("%s ran without a claim", id)
	default:
	}
	// The worker stays, backing off, rather than being replaced every tick.
	s.mu.Lock()
	kept := s.userWorkers["alice"]
	s.mu.Unlock()
	if !kept {
		t.Fatal("alice's worker gone while the claim fails")
	}
	if job, _, _ := s.rt.store.ImportJob(context.Background(), "a1"); job.State != importStateQueued {
		t.Fatalf("a1: %s", job.State)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_claim`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("a1 never ran once the claim could be written")
	}
	waitForImportState(t, s, "a1", importStateFailed)
}

func TestMaxConcurrentImportsReadsTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", defaultMaxConcurrentImports},
		{"  ", defaultMaxConcurrentImports},
		{"1", 1},
		{" 8 ", 8},
		{"0", defaultMaxConcurrentImports},
		{"-2", defaultMaxConcurrentImports},
		{"three", defaultMaxConcurrentImports},
	} {
		getenv := func(key string) string {
			if key == envMaxConcurrentImports {
				return tc.raw
			}
			return ""
		}
		if got := maxConcurrentImports(getenv, nil); got != tc.want {
			t.Errorf("%s=%q: got %d, want %d", envMaxConcurrentImports, tc.raw, got, tc.want)
		}
	}
}

// Tests see a disk with room for any import unless they set one: the real
// disk of whatever machine runs them must not decide whether imports start.
func init() {
	importDiskFree = func(string) (int64, error) { return 1 << 50, nil }
}

// withImportDisk makes the work volume report free bytes for one test.
func withImportDisk(t *testing.T, free func() (int64, error)) {
	t.Helper()
	saved := importDiskFree
	importDiskFree = func(string) (int64, error) { return free() }
	t.Cleanup(func() { importDiskFree = saved })
}

func sizedQueueTestJob(id, user string, size int64) ImportJob {
	job := queueTestJob(id, user)
	job.ExpectedBytes = size
	return job
}

func TestImportWaitsForDiskThatRunningImportsHold(t *testing.T) {
	savedRetry := importClaimRetryMin
	importClaimRetryMin = 10 * time.Millisecond
	t.Cleanup(func() { importClaimRetryMin = savedRetry })
	// Room for one 1 GiB import (2 GiB with its validation copy), not two.
	withImportDisk(t, func() (int64, error) { return importDiskReserve + 3<<30, nil })
	started := make(chan string, 4)
	releaseA := make(chan struct{})
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		started <- job.ID
		if job.ID == "a1" {
			select {
			case <-releaseA:
			case <-ctx.Done():
				return meetingImportResult{}, ctx.Err()
			}
		}
		return meetingImportResult{}, errors.New("test import settled")
	})
	s.importSlots = make(chan struct{}, 3)
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{sizedQueueTestJob("a1", "alice", 1<<30)}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	if id := <-started; id != "a1" {
		t.Fatalf("started %s, want a1", id)
	}
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{sizedQueueTestJob("b1", "bob", 1<<30)}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()

	// Bob's import does not fit while Alice's holds its space: it stays
	// queued, without failing, and without keeping a slot.
	select {
	case id := <-started:
		t.Fatalf("%s started without the disk space for it", id)
	case <-time.After(200 * time.Millisecond):
	}
	if got, _, _ := s.rt.store.ImportJob(context.Background(), "b1"); got.State != importStateQueued {
		t.Fatalf("b1 state %q, want queued", got.State)
	}
	// It takes a slot only for the moment each retry checks the disk, so
	// most of the time just Alice's is held.
	freed := false
	for i := 0; i < 50 && !freed; i++ {
		freed = len(s.importSlots) == 1
		time.Sleep(time.Millisecond)
	}
	if !freed {
		t.Fatalf("the waiting import keeps a slot: %d held", len(s.importSlots))
	}

	// When Alice's settles, Bob's gets the space.
	close(releaseA)
	select {
	case id := <-started:
		if id != "b1" {
			t.Fatalf("started %s, want b1", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("b1 never started after a1 settled")
	}
	waitForImportState(t, s, "b1", importStateFailed)
	s.mu.Lock()
	left := len(s.diskReserved)
	s.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d disk reservations left after every import settled", left)
	}
}

func TestImportTooBigForTheDiskFails(t *testing.T) {
	withImportDisk(t, func() (int64, error) { return importDiskReserve + 1<<30, nil })
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		t.Errorf("%s ran without the disk space for it", job.ID)
		return meetingImportResult{}, nil
	})
	s.importSlots = make(chan struct{}, 3)
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{sizedQueueTestJob("a1", "alice", 1<<30)}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	waitForImportState(t, s, "a1", importStateFailed)
	got, _, _ := s.rt.store.ImportJob(context.Background(), "a1")
	if !strings.Contains(got.Error, "not have enough free disk space") || !strings.Contains(got.Error, "2.0 GiB") {
		t.Fatalf("error %q does not say the disk is short and by how much", got.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(s.importSlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d slots held after the import failed", len(s.importSlots))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUnreadableFreeSpaceDoesNotStopImports(t *testing.T) {
	withImportDisk(t, func() (int64, error) { return 0, errors.New("statfs failed") })
	ran := make(chan struct{}, 1)
	s := queueWorkerService(t, func(ctx context.Context, job ImportJob) (meetingImportResult, error) {
		ran <- struct{}{}
		return meetingImportResult{}, errors.New("test import settled")
	})
	if err := s.rt.store.CreateImportJobs(context.Background(), []ImportJob{queueTestJob("a1", "alice")}); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not run when the free space could not be read")
	}
}
