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

// A Talk backend URL pointing at Cassini's own AppAPI proxy must say so.
//
// It produced "Could not read Talk settings. Check Nextcloud connectivity and
// TLS" — true, and a dead end: the request was sent to the wrong service, and
// connectivity was never the problem. Diagnosed on the demo from the outside.
func TestMisconfiguredTalkBackendURLNamesItselfRatherThanBlamingTheNetwork(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	row := func() readinessCheck {
		t.Helper()
		for _, c := range rt.readiness(context.Background()).Checks {
			if c.ID == "talk.discovery" {
				return c
			}
		}
		t.Fatal("talk.discovery missing")
		return readinessCheck{}
	}

	rt.cfg.TalkBackendURL = "https://nc.test/index.php/apps/app_api/proxy/gocassini"
	c := row()
	if c.State != "needs_action" || c.Code != "talk_backend_url_invalid" {
		t.Fatalf("proxy-shaped backend URL = %+v; want an actionable configuration fault", c)
	}
	if !strings.Contains(c.Message, "CASSINI_TALK_BACKEND_URL") {
		t.Fatalf("the message does not name the setting to change: %q", c.Message)
	}

	// It must not fire on a legitimate subdirectory install.
	rt.cfg.TalkBackendURL = "https://nc.test/nextcloud"
	if c := row(); c.Code == "talk_backend_url_invalid" {
		t.Fatalf("a subdirectory install was rejected: %+v", c)
	}

	// Nor on the documented default, which is to leave it unset.
	rt.cfg.TalkBackendURL = ""
	if c := row(); c.Code == "talk_backend_url_invalid" {
		t.Fatalf("an unset override was reported as a fault: %+v", c)
	}
}

// Every row says whether it can be re-checked on its own, so the panel does not
// keep a second copy of the row-to-probe mapping. A copy in TypeScript drifted
// into a spinner for a probe that never ran.
func TestRowsSayWhetherAProbeCanReCheckThem(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	byID := map[string]readinessCheck{}
	for _, c := range rt.readiness(context.Background()).Checks {
		byID[c.ID] = c
	}

	// Established by a probe.
	for _, id := range []string{"storage", "host", "archive.search"} {
		c, ok := byID[id]
		if !ok {
			t.Fatalf("%s row missing from the report", id)
		}
		if !c.Checkable {
			t.Errorf("%s is established by a probe but says it cannot be re-checked", id)
		}
	}

	// Read from saved configuration. Offering a re-check would wait for nothing.
	for _, id := range []string{"configuration", "talk.authentication", "talk.handoff"} {
		if c, ok := byID[id]; ok && c.Checkable {
			t.Errorf("%s is read from configuration but claims a probe re-checks it", id)
		}
	}
}
