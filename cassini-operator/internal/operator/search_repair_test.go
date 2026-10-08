package operator

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A repair is something the operator does; an unknown one must be refused
// rather than quietly doing nothing, which would look identical to a button
// that works.
func TestRepairRefusesAnActionItCannotPerform(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	for _, body := range []string{`{"action":"rm -rf /"}`, `{"action":""}`, `{}`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/health/repair", strings.NewReader(body))
		rt.readinessHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s -> %d; an unsupported repair must be refused", body, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/health/repair", strings.NewReader("not json"))
	rt.readinessHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unreadable repair request -> %d", rec.Code)
	}
}

// A second click joins the run already in flight. Two concurrent passes over
// the same archive would race each other into the same index for no gain.
func TestSearchBackfillDoesNotStartTwice(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.searchRepair.mu.Lock()
	rt.searchRepair.running = true
	rt.searchRepair.mu.Unlock()
	if rt.startSearchBackfill() {
		t.Fatal("a second backfill started while one was already running")
	}
	rt.searchRepair.mu.Lock()
	rt.searchRepair.running = false
	rt.searchRepair.mu.Unlock()
}

// The row reports the run rather than leaving a reader to guess, and offers no
// button while one is under way.
func TestCoverageRowReportsARunningBackfill(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.searchRepair.mu.Lock()
	rt.searchRepair.running = true
	rt.searchRepair.mu.Unlock()
	defer func() {
		rt.searchRepair.mu.Lock()
		rt.searchRepair.running = false
		rt.searchRepair.mu.Unlock()
	}()
	rt.archiveCoverage.check = readinessCheck{ID: "archive.search", State: "warn", Message: "Some meetings are outside coverage.", Repair: repairBackfillSearch}
	rt.archiveCoverage.taken = true
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		rt.readinessHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var report readinessResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, check := range report.Checks {
			if check.ID != "archive.search" {
				continue
			}
			found = true
			if strings.Count(check.Message, "Re-indexing is running now.") != 1 || check.Repair != "" || !check.Running || len(check.Steps) != 0 {
				t.Fatalf("health poll did not report the running repair: %+v", check)
			}
		}
		if !found {
			t.Fatal("archive check missing")
		}
	}
	if rt.archiveCoverage.check.Repair != repairBackfillSearch || rt.archiveCoverage.check.Running || strings.Contains(rt.archiveCoverage.check.Message, "running") {
		t.Fatalf("reading repair progress changed the cached observation: %+v", rt.archiveCoverage.check)
	}
}

func TestCachedCoverageReportsRepairCompletionAndFailure(t *testing.T) {
	for _, outcome := range []struct {
		name string
		err  error
		want string
	}{
		{"completed", nil, "The last re-index added 2, left 0 unchanged, found 0 not searchable and failed on 0."},
		{"failed", errors.New("archive unavailable"), "The last re-index did not finish: archive unavailable"},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			rt, cleanup := readinessRuntime(t)
			defer cleanup()
			rt.archiveCoverage.check = readinessCheck{ID: "archive.search", State: "warn", Message: "An observed gap.", Repair: repairBackfillSearch}
			rt.archiveCoverage.taken = true
			rt.searchRepair.ran = true
			rt.searchRepair.finished = time.Now()
			rt.searchRepair.report.Indexed = 2
			rt.searchRepair.err = outcome.err
			for i := 0; i < 2; i++ {
				check := rt.lastArchiveCoverage()
				if strings.Count(check.Message, outcome.want) != 1 || check.Repair != repairBackfillSearch || check.RepairFailed != (outcome.err != nil) {
					t.Fatalf("cached coverage did not reflect the repair outcome: %+v", check)
				}
			}
		})
	}
}

func TestAFailedReIndexSaysWhyAndOffersRetryOnlyWhereItCanHelp(t *testing.T) {
	for _, tc := range []struct {
		kind, code, says string
		retry            bool
	}{
		{searchRepairArchiveUnreadable, "search_reindex_archive_unreadable", "could not list the recordings archive in Nextcloud", true},
		{searchRepairIndexUnavailable, "search_reindex_index_unavailable", "could not use the search index", false},
		{searchRepairEnvironment, "search_reindex_environment", "could not read the settings AppAPI gives Cassini", false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			check := readinessCheck{ID: "archive.search", State: "warn", Code: "search_coverage_partial", Message: "9 meetings are searchable.", Repair: repairBackfillSearch}
			describeSearchRepairFailure(&check, searchRepairFailure(tc.kind, errors.New("boom")))
			if check.Code != tc.code || !strings.Contains(check.Message, tc.says) || strings.Contains(check.Message, "boom") {
				t.Fatalf("check = %+v; want %s saying %q, without the raw error", check, tc.code, tc.says)
			}
			if (check.Repair != "") != tc.retry {
				t.Fatalf("retry offered = %v, want %v: a retry that would fail the same way is not a remedy", check.Repair != "", tc.retry)
			}
			last := check.Steps[len(check.Steps)-1]
			if len(last.Commands) != 1 || !strings.Contains(last.Commands[0], "search backfill") {
				t.Fatalf("steps = %+v; the full error should be one copyable command away", check.Steps)
			}
		})
	}
}

func TestAnUnreadableArchiveWaitsOnFailingStorage(t *testing.T) {
	for _, tc := range []struct {
		storage string
		blocked bool
	}{{"needs_action", true}, {"passed", false}} {
		checks := []readinessCheck{
			{ID: "storage", State: tc.storage, Code: "x"},
			{ID: "archive.search", State: "warn", Code: "search_reindex_archive_unreadable", Repair: repairBackfillSearch, RepairFailed: true},
		}
		sortReadinessRows(checks)
		suppressBlockedRows(checks)
		for _, c := range checks {
			if c.ID != "archive.search" {
				continue
			}
			if got := c.Code == "check_blocked"; got != tc.blocked {
				t.Fatalf("storage %s: archive = %+v; blocked = %v, want %v", tc.storage, c, got, tc.blocked)
			}
			if tc.blocked && (c.RepairFailed || c.Repair != "" || !strings.Contains(c.Message, "Recording storage")) {
				t.Fatalf("a waiting row kept its own failure or button: %+v", c)
			}
		}
	}
}

func TestAnUnrecognisedReIndexFailureKeepsItsStepsAndAddsTheLog(t *testing.T) {
	check := readinessCheck{ID: "archive.search", State: "warn", Message: "9 meetings are searchable.", Repair: repairBackfillSearch,
		Steps: []readinessStep{{Label: "Re-index now adds the 3 recordings that are not in search yet"}}}
	describeSearchRepairFailure(&check, errors.New("connection reset"))
	if !strings.Contains(check.Message, "The last re-index did not finish: connection reset") || check.Repair != repairBackfillSearch {
		t.Fatalf("check = %+v; an unrecognised failure keeps its reason and its retry", check)
	}
	if len(check.Steps) != 2 || check.Steps[0].Label != "Re-index now adds the 3 recordings that are not in search yet" {
		t.Fatalf("steps = %+v; the row's own steps must survive", check.Steps)
	}
	if last := check.Steps[1]; len(last.Commands) != 1 || !strings.Contains(last.Commands[0], "search backfill") {
		t.Fatalf("steps = %+v; the log should be one copyable command away", check.Steps)
	}
}
