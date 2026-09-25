package operator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	check := readinessCheck{Message: "Some meetings are outside coverage."}
	rt.describeSearchBackfill(&check, searchCoverage{Untracked: 3})
	if !strings.Contains(check.Message, "Re-indexing is running now.") {
		t.Fatalf("a running repair went unreported: %q", check.Message)
	}
	if check.Repair != "" {
		t.Fatalf("offered a second run while one was in flight: %q", check.Repair)
	}
}
