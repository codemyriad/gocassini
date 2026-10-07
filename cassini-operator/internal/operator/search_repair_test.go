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
