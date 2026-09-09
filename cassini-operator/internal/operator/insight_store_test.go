package operator

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newInsightTestStore(t *testing.T) *insightStore {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &insightStore{db: store.db}
}

func seedInsightRun(t *testing.T, store *insightStore, id, createdBy string) InsightRun {
	t.Helper()
	run := InsightRun{
		ID:              id,
		CreatedBy:       createdBy,
		WorkflowID:      "summarise",
		WorkflowVersion: "v0",
		WorkflowSHA256:  strings.Repeat("a", 64),
		MeetingIDs:      []string{"mtg_1111111111111111", "mtg_2222222222222222"},
		RoomIDs:         []string{"rm_3333333333333333"},
		Question:        "What did we decide?",
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun(%s) error = %v", id, err)
	}
	return run
}

func mustGetInsightRun(t *testing.T, store *insightStore, id string) InsightRun {
	t.Helper()
	run, err := store.GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("GetRun(%s) error = %v", id, err)
	}
	return run
}

func TestInsightStoreCreatesARecordBeforeItsContentExists(t *testing.T) {
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")

	run := mustGetInsightRun(t, store, "ins_0123456789abcdef")
	if run.Status != insightStatusQueued {
		t.Fatalf("status = %q, want %q", run.Status, insightStatusQueued)
	}
	if run.AttemptNumber != 1 {
		t.Fatalf("attempt_number = %d, want 1", run.AttemptNumber)
	}
	if run.Provider != "" || run.Model != "" || run.DocumentPath != "" || run.Error != "" {
		t.Fatalf("a queued run already claims an attempt's results: %#v", run)
	}
	if strings.Join(run.MeetingIDs, ",") != "mtg_1111111111111111,mtg_2222222222222222" {
		t.Fatalf("meeting ids = %v, want them in request order", run.MeetingIDs)
	}
	if strings.Join(run.RoomIDs, ",") != "rm_3333333333333333" {
		t.Fatalf("room ids = %v, want the source rooms", run.RoomIDs)
	}
	if run.CreatedAt.IsZero() || !run.UpdatedAt.Equal(run.CreatedAt) {
		t.Fatalf("timestamps = %v / %v, want a created run stamped once", run.CreatedAt, run.UpdatedAt)
	}
	if attempts, err := store.ListAttempts(context.Background(), run.ID); err != nil || len(attempts) != 0 {
		t.Fatalf("ListAttempts() = %v, %v; a queued run has not attempted anything", attempts, err)
	}
}

func TestInsightStoreRefusesARequestItCouldNotList(t *testing.T) {
	store := newInsightTestStore(t)
	valid := func() InsightRun {
		return InsightRun{
			ID:         "ins_0123456789abcdef",
			CreatedBy:  "alice",
			WorkflowID: "summarise",
			MeetingIDs: []string{"mtg_1111111111111111"},
		}
	}
	cases := map[string]func(*InsightRun){
		"a malformed id":      func(r *InsightRun) { r.ID = "ins_nothex" },
		"an uppercase id":     func(r *InsightRun) { r.ID = "ins_0123456789ABCDEF" },
		"no caller":           func(r *InsightRun) { r.CreatedBy = "  " },
		"no workflow":         func(r *InsightRun) { r.WorkflowID = "" },
		"no meetings":         func(r *InsightRun) { r.MeetingIDs = nil },
		"a blank meeting id":  func(r *InsightRun) { r.MeetingIDs = []string{" "} },
		"a status not queued": func(r *InsightRun) { r.Status = insightStatusRunning },
		"a blank room id":     func(r *InsightRun) { r.RoomIDs = []string{""} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			run := valid()
			mutate(&run)
			if err := store.CreateRun(context.Background(), run); err == nil {
				t.Fatalf("CreateRun(%s) = nil, want a refusal", name)
			}
		})
	}
}

func TestInsightStoreListsOnlyTheCallersRunsNewestFirst(t *testing.T) {
	store := newInsightTestStore(t)
	base := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	for i, id := range []string{"ins_00000000000000a1", "ins_00000000000000a2", "ins_00000000000000a3"} {
		run := seedInsightRun(t, store, id, "alice")
		run.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if _, err := store.db.Exec(`UPDATE insight_runs SET created_at = ? WHERE id = ?`, formatUTCString(run.CreatedAt), id); err != nil {
			t.Fatalf("restamp %s: %v", id, err)
		}
	}
	seedInsightRun(t, store, "ins_00000000000000b1", "bob")

	runs, err := store.ListRuns(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	var ids []string
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	want := "ins_00000000000000a3,ins_00000000000000a2,ins_00000000000000a1"
	if strings.Join(ids, ",") != want {
		t.Fatalf("alice's runs = %v, want %s", ids, want)
	}

	empty, err := store.ListRuns(context.Background(), "carol")
	if err != nil {
		t.Fatalf("ListRuns(carol) error = %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ListRuns(carol) = %#v, want an empty list rather than nil", empty)
	}
	if _, err := store.ListRuns(context.Background(), " "); err == nil {
		t.Fatal("ListRuns() with no caller = nil error, want a refusal rather than everybody's runs")
	}
}

func TestInsightStoreGetRunReportsAnAbsentRunAsNoRows(t *testing.T) {
	store := newInsightTestStore(t)
	if _, err := store.GetRun(context.Background(), "ins_0123456789abcdef"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetRun() error = %v, want sql.ErrNoRows", err)
	}
}

func TestInsightStoreFirstAttemptKeepsTheRunAtAttemptOne(t *testing.T) {
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")

	run, err := store.BeginAttempt(context.Background(), "ins_0123456789abcdef")
	if err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if run.Status != insightStatusRunning || run.AttemptNumber != 1 {
		t.Fatalf("first attempt = %s/%d, want running/1", run.Status, run.AttemptNumber)
	}
	attempts, err := store.ListAttempts(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].AttemptNumber != 1 || attempts[0].Status != insightStatusRunning {
		t.Fatalf("attempts = %#v, want one running attempt 1", attempts)
	}
	if attempts[0].FinishedAt != nil {
		t.Fatalf("attempt finished_at = %v, want unset while it runs", attempts[0].FinishedAt)
	}
	if _, err := store.BeginAttempt(context.Background(), "ins_ffffffffffffffff"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("BeginAttempt(absent) error = %v, want sql.ErrNoRows", err)
	}
}

func TestInsightStoreRetryIsAnotherAttemptOnTheSameRun(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")

	if _, err := store.BeginAttempt(ctx, "ins_0123456789abcdef"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if err := store.FinishAttempt(ctx, "ins_0123456789abcdef", InsightOutcome{
		Status:   insightStatusFailed,
		Provider: "openrouter",
		Model:    "anthropic/claude",
		Error:    "the provider returned 401",
	}); err != nil {
		t.Fatalf("FinishAttempt(failed) error = %v", err)
	}

	failed := mustGetInsightRun(t, store, "ins_0123456789abcdef")
	if failed.Status != insightStatusFailed || failed.Error != "the provider returned 401" {
		t.Fatalf("failed run = %#v, want a failure a user can act on", failed)
	}
	if failed.Provider != "openrouter" || failed.Model != "anthropic/claude" {
		t.Fatalf("failed run endpoint = %s/%s, want the one the attempt resolved", failed.Provider, failed.Model)
	}

	retried, err := store.BeginAttempt(ctx, "ins_0123456789abcdef")
	if err != nil {
		t.Fatalf("BeginAttempt(retry) error = %v", err)
	}
	if retried.ID != failed.ID {
		t.Fatalf("retry id = %s, want the same run %s", retried.ID, failed.ID)
	}
	if retried.AttemptNumber != 2 || retried.Status != insightStatusRunning {
		t.Fatalf("retry = %s/%d, want running/2", retried.Status, retried.AttemptNumber)
	}
	// The endpoint is re-resolved from current settings, so nothing the failed
	// attempt used may still be showing while the retry runs.
	if retried.Provider != "" || retried.Model != "" || retried.Error != "" || retried.DocumentPath != "" {
		t.Fatalf("retry still carries the failed attempt's results: %#v", retried)
	}
	if retried.WorkflowID != failed.WorkflowID || retried.Question != failed.Question ||
		strings.Join(retried.MeetingIDs, ",") != strings.Join(failed.MeetingIDs, ",") {
		t.Fatalf("retry changed the request: %#v", retried)
	}

	if err := store.FinishAttempt(ctx, "ins_0123456789abcdef", InsightOutcome{
		Status:       insightStatusSucceeded,
		Provider:     "local",
		Model:        "qwen3",
		DocumentPath: "Cassini/insights/ins_0123456789abcdef.md",
	}); err != nil {
		t.Fatalf("FinishAttempt(succeeded) error = %v", err)
	}
	done := mustGetInsightRun(t, store, "ins_0123456789abcdef")
	if done.Status != insightStatusSucceeded || done.DocumentPath == "" || done.Provider != "local" {
		t.Fatalf("succeeded run = %#v, want the attempt that produced the bytes", done)
	}

	attempts, err := store.ListAttempts(ctx, "ins_0123456789abcdef")
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want one card with two attempts", len(attempts))
	}
	if attempts[0].AttemptNumber != 2 || attempts[0].Status != insightStatusSucceeded || attempts[0].Provider != "local" {
		t.Fatalf("latest attempt = %#v", attempts[0])
	}
	if attempts[1].AttemptNumber != 1 || attempts[1].Status != insightStatusFailed || attempts[1].Provider != "openrouter" {
		t.Fatalf("first attempt = %#v, want the endpoint it actually used kept", attempts[1])
	}
	if attempts[1].FinishedAt == nil || attempts[0].FinishedAt == nil {
		t.Fatalf("finished attempts have no finished_at: %#v", attempts)
	}
}

func TestInsightStoreBeginAttemptRefusesARunThatIsNotQueuedOrFailed(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")

	if _, err := store.BeginAttempt(ctx, "ins_0123456789abcdef"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if _, err := store.BeginAttempt(ctx, "ins_0123456789abcdef"); !errors.Is(err, errInsightRunBusy) {
		t.Fatalf("BeginAttempt(running) error = %v, want errInsightRunBusy", err)
	}

	if err := store.FinishAttempt(ctx, "ins_0123456789abcdef", InsightOutcome{
		Status:       insightStatusSucceeded,
		DocumentPath: "Cassini/insights/ins_0123456789abcdef.md",
	}); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	// A succeeded run is terminal: asking the same meetings a different question
	// makes a new record and never touches this one.
	if _, err := store.BeginAttempt(ctx, "ins_0123456789abcdef"); !errors.Is(err, errInsightRunBusy) {
		t.Fatalf("BeginAttempt(succeeded) error = %v, want errInsightRunBusy", err)
	}
	if run := mustGetInsightRun(t, store, "ins_0123456789abcdef"); run.AttemptNumber != 1 {
		t.Fatalf("refused retries bumped the attempt number to %d", run.AttemptNumber)
	}
}

// TestInsightStoreBeginAttemptStartsExactlyOnceUnderConcurrency is the one
// property in this store a reader cannot check by eye: two retries pressed at
// the same moment must produce one attempt and one refusal, never two attempts
// writing the same document path.
//
// The store's pool is capped at one connection (OpenStore), so these goroutines
// race in Go and queue in SQLite. That is what the operator actually runs, and
// what the test therefore proves; the compare-and-swap in BeginAttempt is what
// would still hold if the cap were lifted.
func TestInsightStoreBeginAttemptStartsExactlyOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")
	if _, err := store.BeginAttempt(ctx, "ins_0123456789abcdef"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if err := store.FinishAttempt(ctx, "ins_0123456789abcdef", InsightOutcome{
		Status: insightStatusFailed,
		Error:  "the provider returned 401",
	}); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}

	const racers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	results := make([]error, racers)
	numbers := make([]int, racers)
	for i := 0; i < racers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			run, err := store.BeginAttempt(ctx, "ins_0123456789abcdef")
			results[i] = err
			numbers[i] = run.AttemptNumber
		}(i)
	}
	start.Done()
	done.Wait()

	started, busy := 0, 0
	for i, err := range results {
		switch {
		case err == nil:
			started++
			if numbers[i] != 2 {
				t.Fatalf("the winning retry began attempt %d, want 2", numbers[i])
			}
		case errors.Is(err, errInsightRunBusy):
			busy++
		default:
			t.Fatalf("BeginAttempt() error = %v, want nil or errInsightRunBusy", err)
		}
	}
	if started != 1 || busy != racers-1 {
		t.Fatalf("%d concurrent retries started %d attempts and refused %d, want 1 and %d", racers, started, busy, racers-1)
	}

	run := mustGetInsightRun(t, store, "ins_0123456789abcdef")
	if run.Status != insightStatusRunning || run.AttemptNumber != 2 {
		t.Fatalf("run after the race = %s/%d, want running/2", run.Status, run.AttemptNumber)
	}
	attempts, err := store.ListAttempts(ctx, "ins_0123456789abcdef")
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt rows = %d, want the first attempt plus one retry", len(attempts))
	}
}

func TestInsightStoreFinishAttemptRefusesAnUnusableOutcome(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")
	if _, err := store.BeginAttempt(ctx, "ins_0123456789abcdef"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}

	cases := map[string]InsightOutcome{
		"an unknown status":         {Status: "interrupted", Error: "x"},
		"queued as an outcome":      {Status: insightStatusQueued},
		"success without the bytes": {Status: insightStatusSucceeded},
		"failure without a reason":  {Status: insightStatusFailed, Error: "   "},
	}
	for name, outcome := range cases {
		t.Run(name, func(t *testing.T) {
			if err := store.FinishAttempt(ctx, "ins_0123456789abcdef", outcome); err == nil {
				t.Fatalf("FinishAttempt(%s) = nil, want a refusal", name)
			}
		})
	}
	if run := mustGetInsightRun(t, store, "ins_0123456789abcdef"); run.Status != insightStatusRunning {
		t.Fatalf("status after refused outcomes = %q, want it still running", run.Status)
	}
}

func TestInsightStoreFinishAttemptRefusesARunThatIsNotRunning(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_0123456789abcdef", "alice")

	outcome := InsightOutcome{Status: insightStatusSucceeded, DocumentPath: "Cassini/insights/x.md"}
	if err := store.FinishAttempt(ctx, "ins_0123456789abcdef", outcome); !errors.Is(err, errInsightRunNotRunning) {
		t.Fatalf("FinishAttempt(queued) error = %v, want errInsightRunNotRunning", err)
	}
	if err := store.FinishAttempt(ctx, "ins_ffffffffffffffff", outcome); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("FinishAttempt(absent) error = %v, want sql.ErrNoRows", err)
	}
}

func TestInsightStoreFailsRunsAnOperatorRestartStranded(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)
	seedInsightRun(t, store, "ins_00000000000000a1", "alice")
	seedInsightRun(t, store, "ins_00000000000000a2", "alice")
	seedInsightRun(t, store, "ins_00000000000000a3", "alice")
	if _, err := store.BeginAttempt(ctx, "ins_00000000000000a2"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if _, err := store.BeginAttempt(ctx, "ins_00000000000000a3"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if err := store.FinishAttempt(ctx, "ins_00000000000000a3", InsightOutcome{
		Status:       insightStatusSucceeded,
		DocumentPath: "Cassini/insights/ins_00000000000000a3.md",
	}); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}

	// Backdate everything written so far: this is what the crash left behind.
	// The run created after the cutoff is the live process's own work.
	backdate := formatUTCString(time.Now().Add(-time.Hour))
	if _, err := store.db.Exec(`UPDATE insight_runs SET updated_at = ?`, backdate); err != nil {
		t.Fatalf("backdate runs: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE insight_run_attempts SET started_at = ?`, backdate); err != nil {
		t.Fatalf("backdate attempts: %v", err)
	}
	cutoff := time.Now()
	seedInsightRun(t, store, "ins_00000000000000b1", "bob")
	if _, err := store.BeginAttempt(ctx, "ins_00000000000000b1"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}

	swept, err := store.MarkInterruptedRunsFailed(ctx, cutoff)
	if err != nil {
		t.Fatalf("MarkInterruptedRunsFailed() error = %v", err)
	}
	if swept != 2 {
		t.Fatalf("swept %d runs, want the queued and the running one", swept)
	}
	for _, id := range []string{"ins_00000000000000a1", "ins_00000000000000a2"} {
		run := mustGetInsightRun(t, store, id)
		if run.Status != insightStatusFailed || !strings.Contains(run.Error, "restarted") {
			t.Fatalf("%s = %s/%q, want a failure that says to retry it", id, run.Status, run.Error)
		}
	}
	if run := mustGetInsightRun(t, store, "ins_00000000000000a3"); run.Status != insightStatusSucceeded {
		t.Fatalf("the sweep touched a finished run: %#v", run)
	}
	if run := mustGetInsightRun(t, store, "ins_00000000000000b1"); run.Status != insightStatusRunning {
		t.Fatalf("the sweep failed a run this process started: %#v", run)
	}
	attempts, err := store.ListAttempts(ctx, "ins_00000000000000a2")
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != insightStatusFailed || attempts[0].FinishedAt == nil {
		t.Fatalf("stranded attempt = %#v, want it closed too", attempts)
	}

	// A swept run is retryable, which is the point of failing it rather than
	// leaving it wedged at running for ever.
	retried, err := store.BeginAttempt(ctx, "ins_00000000000000a2")
	if err != nil {
		t.Fatalf("BeginAttempt(after sweep) error = %v", err)
	}
	if retried.AttemptNumber != 2 {
		t.Fatalf("retry after sweep = attempt %d, want 2", retried.AttemptNumber)
	}
}

// TestInsightStoreSweepsStrandedRunsByStaleness covers the repair the store
// makes on its own, and the reason it is keyed on staleness rather than on
// process start: a run this LIVE process wrote and then failed to move has to
// become retryable too, where a sweep that latched after its first read left it
// wedged at 409 until the next restart.
func TestInsightStoreSweepsStrandedRunsByStaleness(t *testing.T) {
	ctx := context.Background()
	store := newInsightTestStore(t)

	// The two package-level knobs the sweep reads. Both are restored, because a
	// test that moved them permanently would silently disarm every later one.
	realStart := insightProcessStartedAt
	insightSweep.Lock()
	realLast := insightSweep.last
	insightSweep.Unlock()
	t.Cleanup(func() {
		insightProcessStartedAt = realStart
		insightSweep.Lock()
		insightSweep.last = realLast
		insightSweep.Unlock()
	})
	// Forget the throttle, so the next read actually sweeps.
	rearm := func() {
		t.Helper()
		insightSweep.Lock()
		insightSweep.last = time.Time{}
		insightSweep.Unlock()
	}
	running := func(id string, lastWritten time.Time) {
		t.Helper()
		seedInsightRun(t, store, id, "alice")
		if _, err := store.BeginAttempt(ctx, id); err != nil {
			t.Fatalf("BeginAttempt(%s) error = %v", id, err)
		}
		if _, err := store.db.Exec(`UPDATE insight_runs SET updated_at = ? WHERE id = ?`,
			formatUTCString(lastWritten), id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	// A crash: written before this process existed, so nothing is going to write
	// it again whatever its age.
	running("ins_00000000000000a1", insightProcessStartedAt.Add(-time.Hour))
	rearm()
	if run := mustGetInsightRun(t, store, "ins_00000000000000a1"); run.Status != insightStatusFailed {
		t.Fatalf("a run left behind by a dead process is still %q", run.Status)
	}

	// The throttle: the sweep is a write transaction on the polling path, so a
	// second read moments later does not pay for it again.
	running("ins_00000000000000a2", insightProcessStartedAt.Add(-time.Hour))
	if run := mustGetInsightRun(t, store, "ins_00000000000000a2"); run.Status != insightStatusRunning {
		t.Fatalf("the sweep ran twice inside its own interval, moving a run to %q", run.Status)
	}
	rearm()
	if run := mustGetInsightRun(t, store, "ins_00000000000000a2"); run.Status != insightStatusFailed {
		t.Fatalf("the sweep latched: a stranded run is still %q on a later read", run.Status)
	}

	// The live-process wedge, which the old process-start criterion could never
	// reach. This process has been up for hours; a row it wrote itself and then
	// stopped writing is past any deadline an attempt has, so it is stranded.
	insightProcessStartedAt = time.Now().Add(-3 * time.Hour)
	running("ins_00000000000000b1", time.Now().Add(-(insightRunTimeout + insightSweepGrace + time.Minute)))
	// And one this process is plausibly still working on, which must survive the
	// same sweep.
	running("ins_00000000000000b2", time.Now().Add(-time.Minute))
	rearm()
	if run := mustGetInsightRun(t, store, "ins_00000000000000b1"); run.Status != insightStatusFailed {
		t.Fatalf("a run this process stopped writing is still %q, so retry stays a 409 for ever", run.Status)
	}
	if run := mustGetInsightRun(t, store, "ins_00000000000000b2"); run.Status != insightStatusRunning {
		t.Fatalf("the sweep failed a live attempt at %q", run.Status)
	}

	// The point of failing a stranded run rather than leaving it: it is retryable.
	retried, err := store.BeginAttempt(ctx, "ins_00000000000000b1")
	if err != nil {
		t.Fatalf("BeginAttempt(after sweep) error = %v", err)
	}
	if retried.AttemptNumber != 2 {
		t.Fatalf("retry after sweep = attempt %d, want 2", retried.AttemptNumber)
	}
}

// TestInsightRunsAreUntouchedByTheJobMachinery is the trap D-720 names. Both
// halves are checked in the code they describe: SetAttemptStageLogPath rejects
// any stage outside record|build|seal|publish, and MarkIncompleteJobsInterrupted
// updates jobs and job_attempts only.
func TestInsightRunsAreUntouchedByTheJobMachinery(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	insights := &insightStore{db: store.db}

	seedJobRow(t, store.db, seededJobRow{ID: "job1", Stage: "record", State: "running", CreatedAt: "2026-09-03T10:00:00.000000000Z"})
	if err := store.SetAttemptStageLogPath(ctx, "job1", 1, "insight", "/work/logs/x.log"); err == nil {
		t.Fatal("SetAttemptStageLogPath(insight) = nil, want a refusal: there is no insight stage on a job")
	}

	seedInsightRun(t, insights, "ins_00000000000000a1", "alice")
	seedInsightRun(t, insights, "ins_00000000000000a2", "alice")
	if _, err := insights.BeginAttempt(ctx, "ins_00000000000000a2"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}

	if _, err := store.MarkIncompleteJobsInterrupted(ctx, nowUTCString()); err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if run := mustGetInsightRun(t, insights, "ins_00000000000000a1"); run.Status != insightStatusQueued {
		t.Fatalf("the job restart sweep moved a queued insight to %q", run.Status)
	}
	if run := mustGetInsightRun(t, insights, "ins_00000000000000a2"); run.Status != insightStatusRunning {
		t.Fatalf("the job restart sweep moved a running insight to %q", run.Status)
	}
}
