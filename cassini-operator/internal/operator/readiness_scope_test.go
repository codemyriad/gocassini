package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Every row maps to the one probe that establishes it, so retrying a row costs
// that probe and nothing else.
func TestReadinessScopeMapsRowsToTheProbeBehindThem(t *testing.T) {
	for _, tc := range []struct {
		ids  []string
		want readinessScope
	}{
		{[]string{"storage"}, readinessScope{storage: true}},
		{[]string{"host"}, readinessScope{host: true}},
		{[]string{"host.workdir"}, readinessScope{host: true}},
		{[]string{"talk.discovery"}, readinessScope{talk: true}},
		{[]string{"talk.hpb"}, readinessScope{talk: true}},
		{[]string{"archive.search"}, readinessScope{archive: true}},
		{[]string{"host.workdir", "archive.search"}, readinessScope{host: true, archive: true}},
	} {
		if got := readinessScopeFor(tc.ids); got != tc.want {
			t.Errorf("readinessScopeFor(%v) = %+v, want %+v", tc.ids, got, tc.want)
		}
	}

	// Read from saved configuration, not probed. Naming one of these asks for
	// work that does not exist, and must not quietly widen into everything.
	for _, id := range []string{"configuration", "talk.authentication", "talk.handoff", "test", "nonsense"} {
		if got := readinessScopeFor([]string{id}); !got.empty() {
			t.Errorf("readinessScopeFor(%q) = %+v; nothing probes that row", id, got)
		}
	}
}

// A scoped request must not run the probes it did not ask for. The media doctor
// is the observable one: it is a subprocess, so a runtime with no binary reports
// host_checks_unavailable the moment it runs.
func TestScopedCheckRunsOnlyTheProbesItNames(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = ""

	rt.checkRecordingReadinessScoped(context.Background(), readinessScope{archive: true})
	rt.recordingSetup.mu.Lock()
	host := append([]readinessCheck(nil), rt.recordingSetup.hostChecks...)
	rt.recordingSetup.mu.Unlock()
	if len(host) != 0 {
		t.Fatalf("an archive-only check ran the media doctor: %+v", host)
	}

	rt.checkRecordingReadinessScoped(context.Background(), readinessScope{host: true})
	rt.recordingSetup.mu.Lock()
	host = append([]readinessCheck(nil), rt.recordingSetup.hostChecks...)
	rt.recordingSetup.mu.Unlock()
	if len(host) == 0 {
		t.Fatal("a host check did not run the media doctor")
	}
}

// The coalescing window is per scope. One row's retry must not make another
// row's, a moment later, silently do nothing.
func TestProbeCoalescingIsHeldPerScope(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	s := &rt.recordingSetup
	now := time.Now()
	if !s.beginProbe("host", now) {
		t.Fatal("first host probe was refused")
	}
	if s.beginProbe("host", now.Add(probeCoalesceWindow/2)) {
		t.Fatal("a duplicate click within the window was not coalesced")
	}
	if !s.beginProbe("archive", now.Add(probeCoalesceWindow/2)) {
		t.Fatal("the host window blocked a different scope")
	}
	if !s.beginProbe("host", now.Add(2*probeCoalesceWindow)) {
		t.Fatal("the window never reopened")
	}
}

// A request naming only rows nothing probes is refused, not widened.
func TestScopedCheckRefusesARequestWithNoProbeBehindIt(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/health/check", strings.NewReader(`{"only":["configuration"]}`))
	rt.readinessHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unprobeable scope -> %d; want a refusal", rec.Code)
	}

	// An absent body still means "run everything", which is what the panel's
	// Run all checks button sends.
	rec = httptest.NewRecorder()
	rt.readinessHandler(rec, httptest.NewRequest(http.MethodPost, "/health/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("bodyless check -> %d; want the full run", rec.Code)
	}
}
