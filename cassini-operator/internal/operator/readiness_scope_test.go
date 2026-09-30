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

func TestSetupEditsInvalidateTalkProbeCoalescing(t *testing.T) {
	for _, edit := range []string{`{"internal_secret":"changed"}`, `{"test_room_url":"https://cloud.test/call/anotherroom"}`} {
		t.Run(edit, func(t *testing.T) {
			rt, cleanup := readinessRuntime(t)
			defer cleanup()
			putRecordingSetup(t, rt, `{"internal_secret":"old","test_room_url":"https://cloud.test/call/testroom"}`, http.StatusOK)
			probes := 0
			rt.recordingSetup.probe = func(context.Context, string) ([]readinessCheck, error) {
				probes++
				return []readinessCheck{{ID: "talk.hpb", State: "passed"}}, nil
			}
			rt.checkRecordingReadinessScoped(context.Background(), readinessScope{talk: true})
			putRecordingSetup(t, rt, edit, http.StatusOK)
			rt.checkRecordingReadinessScoped(context.Background(), readinessScope{talk: true})
			if probes != 2 || len(rt.recordingSetup.checks) != 1 {
				t.Fatalf("changed setup was not probed: calls=%d, cached checks=%+v", probes, rt.recordingSetup.checks)
			}
		})
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

// The operator must accept `warn` from the connection probe. Rejecting it is
// what forced "tried and could not reach it" to report itself as an absence:
// the recorder could not say warn without the whole probe being thrown away as
// an invalid state (D-798).
func TestConnectionProbeAcceptsWarnFromTheRecorder(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = writeFakeDoctorBin(t,
		`[{"id":"talk.discovery","state":"warn","code":"nextcloud_unreachable","message":"Could not read Talk settings."}]`)

	checks, err := rt.runConnectionProbe(context.Background(), "https://nc.test/call/room")
	if err != nil {
		t.Fatalf("a warning finding was rejected: %v", err)
	}
	if len(checks) != 1 || checks[0].State != "warn" {
		t.Fatalf("checks = %+v; want the warn finding preserved", checks)
	}

	// A state the ladder does not know is still refused: an operator that does
	// not understand the answer must not treat it as healthy.
	rt.cfg.CassiniBin = writeFakeDoctorBin(t,
		`[{"id":"talk.discovery","state":"probably-fine","code":"x","message":"y"}]`)
	if _, err := rt.runConnectionProbe(context.Background(), "https://nc.test/call/room"); err == nil {
		t.Fatal("an unknown probe state was accepted")
	}
}
