package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	for _, id := range []string{"configuration", "talk.handoff", "test", "nonsense"} {
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
	for _, id := range []string{"storage", "host.workdir", "archive.search"} {
		c, ok := byID[id]
		if !ok {
			t.Fatalf("%s row missing from the report", id)
		}
		if !c.Checkable {
			t.Errorf("%s is established by a probe but says it cannot be re-checked", id)
		}
	}

	// Read from saved configuration. Offering a re-check would wait for nothing.
	for _, id := range []string{"configuration", "talk.handoff"} {
		if c, ok := byID[id]; ok && c.Checkable {
			t.Errorf("%s is read from configuration but claims a probe re-checks it", id)
		}
	}
}

// No High Performance Backend stops recording outright, so it is needs_action
// rather than a warning — and it must be discoverable without the things the
// connection probe needs.
//
// The probe learns the same fact from Talk's recording settings, which require
// the recording credential AND a test room. On the local harness neither held,
// the probe returned early at talk.discovery, and an install that could not
// record at all reported no backend problem whatsoever.
func TestNoHPBIsAFailureAndNamesWhereToReadAboutIt(t *testing.T) {
	for mode, want := range map[string]string{
		"internal": "needs_action",
		// A declared backend is a FACT worth reporting, even though the
		// connection probe is what verifies it. Returning nothing here meant
		// the row vanished whenever the probe stopped earlier in the chain.
		"external": "not_verified",
		// Talk absent or too old to say, and a mode this build cannot
		// interpret: nothing to conclude, and the report supplies its own
		// unchecked row rather than this one guessing.
		"":         "",
		"sideways": "",
	} {
		check := hpbCheckForMode(mode)
		if want == "" {
			if check != nil {
				t.Fatalf("signaling mode %q produced %+v; nothing can be concluded from it", mode, check)
			}
			continue
		}
		if check == nil {
			t.Fatalf("signaling mode %q produced no finding", mode)
		}
		if check.State != want {
			t.Errorf("mode %q = %q, want %q", mode, check.State, want)
		}
		if check.State == "needs_action" && check.Docs == "" {
			t.Error("a fault the operator cannot repair must say where to read about it")
		}
		if strings.Contains(check.Message, "checked separately") {
			t.Error("the message points at a check that does not happen")
		}
	}
}

// The row that decides whether recording can work at all must be ON the
// checklist, always. It was the only row with no way to say "nobody has
// established this", so it was simply absent — after a restart, after a check
// that could not reach Nextcloud, and whenever the probe stopped earlier in the
// chain. The rows depending on it then stopped waiting for it, because there
// was nothing there to wait for.
func TestTheBackendRowIsAlwaysOnTheChecklist(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	// No probe has run: exactly the state a freshly booted operator is in.
	report := rt.readiness(context.Background())
	var row readinessCheck
	for _, c := range report.Checks {
		if c.ID == "talk.hpb" {
			row = c
		}
	}
	if row.ID == "" {
		t.Fatal("no talk.hpb row at all; the most important check cannot be the one that disappears")
	}
	if row.State != "not_verified" || row.Code != "hpb_not_checked" {
		t.Fatalf("talk.hpb = %+v; want an unchecked row, not a verdict", row)
	}
	if row.Action != "recheck" {
		t.Errorf("an unchecked row offers no way to check it: %+v", row)
	}
	// And it must not be invented twice when a probe already reported one. With
	// a secret saved, since a missing one is itself a verdict for this row.
	t.Setenv(envTalkSignalingInternalSecret, "saved-secret")
	rt.recordingSetup.checks = []readinessCheck{{ID: "talk.hpb", State: "passed", Code: "hpb_authenticated"}}
	rt.recordingSetup.checkedAt = time.Now()
	seen := 0
	for _, c := range rt.readiness(context.Background()).Checks {
		if c.ID == "talk.hpb" {
			seen++
			if c.Code != "hpb_authenticated" {
				t.Errorf("a real finding was replaced by the placeholder: %+v", c)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("%d talk.hpb rows; the placeholder must stand in, not pile on", seen)
	}
}

// A row that asks for a secret must say where the secret lives, and must not
// ask at all when there is nothing for it to authenticate to.
//
// Both halves were reported from the local harness: "I can't understand where
// should i recover the talk signaling server internal secret" — on a stack with
// no signaling server at all. The guidance existed, in the startup log and
// /status, and never reached the row that asks the question.
func TestTheBackendRowCarriesTheCredentialItAuthenticatesWith(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	row := func() readinessCheck {
		t.Helper()
		for _, c := range rt.readiness(context.Background()).Checks {
			if c.ID == "talk.hpb" {
				return c
			}
		}
		t.Fatal("talk.hpb missing")
		return readinessCheck{}
	}

	// No High Performance Backend: there is nothing to authenticate to, and the
	// backend row is the one to act on.
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checks = []readinessCheck{{ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled"}}
	rt.recordingSetup.checkedAt = time.Now()
	rt.recordingSetup.mu.Unlock()
	// The row stays the backend's own fault; the credential is not mentioned,
	// because there is nothing for it to authenticate to and asking for it is
	// how one missing backend became two rows wanting attention.
	if c := row(); c.Code != "hpb_disabled" {
		t.Fatalf("row with no backend = %+v; want the backend fault, unembellished", c)
	}

	// A backend exists: now the secret is genuinely required, and the row has to
	// say where to read it.
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checks = []readinessCheck{{ID: "talk.hpb", State: "passed", Code: "hpb_authenticated"}}
	rt.recordingSetup.mu.Unlock()
	c := row()
	if c.State != "needs_action" || c.Code != "internal_secret_missing" {
		t.Fatalf("row with a backend = %+v; the secret is required", c)
	}
	if len(c.Steps) < 2 {
		t.Fatalf("the row does not say where the secret lives: %+v", c.Steps)
	}
	var joined string
	for _, s := range c.Steps {
		joined += s.Label + " "
	}
	// The misconception this copy exists to kill: an administrator reading
	// "internal credential" went looking in Nextcloud's configuration, where
	// D-447 verified the value appears in zero appconfig keys and zero entries
	// of occ config:list. It belongs to the signaling server.
	if !strings.Contains(c.Message, "not a Nextcloud setting") {
		t.Errorf("the message does not say whose credential this is: %q", c.Message)
	}
	for _, want := range []string{"INTERNAL_SECRET", "internalsecret"} {
		if !strings.Contains(joined, want) {
			t.Errorf("steps never name %q, which is what an administrator searches for: %q", want, joined)
		}
	}
}

// An unchecked row and its checked self must be the SAME row.
//
// The placeholder used to be a single "host" row, which the first check replaced
// with host.workdir and host.tmpdir.writable — so "Recording host" appeared to
// rename itself to "Recording volume" and "Temporary space". A row keeps its
// identity and changes its verdict; anything else reads as the panel rearranging
// itself under the reader.
func TestUncheckedHostRowsAreTheRowsACheckProduces(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	hostIDs := func() []string {
		t.Helper()
		var ids []string
		for _, c := range rt.readiness(context.Background()).Checks {
			if c.ID == "host" || strings.HasPrefix(c.ID, "host.") {
				ids = append(ids, c.ID)
			}
		}
		return ids
	}

	before := hostIDs()
	rt.cfg.CassiniBin = writeFakeDoctorBin(t, `[
		{"id":"workdir","status":"ok","summary":"working directory /work"},
		{"id":"workdir.writable","status":"ok","summary":"writable"},
		{"id":"tmpdir.writable","status":"ok","summary":"writable"}
	]`)
	rt.checkRecordingReadinessScoped(context.Background(), readinessScope{host: true})
	after := hostIDs()

	if len(before) != len(after) {
		t.Fatalf("host rows changed shape on check: before=%v after=%v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("row %d changed identity on check: %q -> %q", i, before[i], after[i])
		}
	}
}

// The backend comes before the connection it carries.
//
// The talk rows are a dependency chain and should read as one: a backend has to
// exist before Cassini can authenticate to it, and only then can the connection
// be verified. Assembled order once put them the other way round, so a reader
// met the consequence above its cause.
func TestBackendRowComesBeforeTheConnectionItCarries(t *testing.T) {
	checks := []readinessCheck{
		{ID: "archive.search"},
		{ID: "talk.discovery"},
		{ID: "storage"},
		{ID: "talk.hpb"},
		{ID: "host.workdir"},
		{ID: "something.new"},
	}
	sortReadinessRows(checks)
	var got []string
	for _, c := range checks {
		got = append(got, c.ID)
	}
	want := []string{"host.workdir", "storage", "talk.hpb", "talk.discovery", "archive.search", "something.new"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// An id the order does not know lands at the end rather than vanishing: a
	// new check should appear unannounced, not disappear.
	if got[len(got)-1] != "something.new" {
		t.Fatalf("an unknown row did not survive sorting: %v", got)
	}
}

// One missing backend should produce ONE row asking for attention.
//
// Marco: "why should i give attention to the 'Talk connection' in the first
// place in this scenario?" Reported independently, a missing High Performance
// Backend produced three rows wanting something — the backend, the credential,
// and the connection — of which only the first could be acted on.
func TestOneMissingPrerequisiteProducesOneActionableRow(t *testing.T) {
	checks := []readinessCheck{
		{ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled", Docs: "https://example.invalid/"},
		{ID: "talk.discovery", State: "needs_action", Code: "recording_auth_rejected", Action: "recheck"},
		{ID: "storage", State: "passed", Code: "storage_ready"},
	}
	sortReadinessRows(checks)
	suppressBlockedRows(checks)

	actionable := 0
	for _, c := range checks {
		if c.State == "needs_action" {
			actionable++
			if c.ID != "talk.hpb" {
				t.Errorf("%s still demands attention; only the backend can be acted on", c.ID)
			}
		}
	}
	if actionable != 1 {
		t.Fatalf("%d rows want attention; exactly one thing is wrong", actionable)
	}

	for _, c := range checks {
		if c.ID != "talk.discovery" {
			continue
		}
		if c.Code != "check_blocked" {
			t.Fatalf("talk.discovery = %+v; want it to say what it waits for", c)
		}
		if !strings.Contains(c.Message, "High Performance Backend") {
			t.Errorf("the blocked row does not name its blocker: %q", c.Message)
		}
		// A remedy on a blocked row splits one fix across rows.
		if c.Action != "" || len(c.Steps) != 0 || c.Repair != "" {
			t.Errorf("a blocked row kept a remedy of its own: %+v", c)
		}
	}
}

// A fault a row owns regardless of its prerequisites must survive. Nobody else
// reports a Talk backend URL pointing at Cassini's own proxy, and it is wrong
// whether or not a backend exists.
func TestAnIndependentFaultIsNotSuppressed(t *testing.T) {
	checks := []readinessCheck{
		{ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled"},
		{ID: "talk.discovery", State: "needs_action", Code: "talk_backend_url_invalid"},
	}
	sortReadinessRows(checks)
	suppressBlockedRows(checks)
	for _, c := range checks {
		if c.ID == "talk.discovery" && c.Code != "talk_backend_url_invalid" {
			t.Fatalf("an independent misconfiguration was suppressed: %+v", c)
		}
	}
}

// A prerequisite nobody has CHECKED suppresses nothing: no conclusion follows
// from a check that has not run, and hiding a finding on that basis loses it.
func TestAnUncheckedPrerequisiteSuppressesNothing(t *testing.T) {
	checks := []readinessCheck{
		{ID: "talk.hpb", State: "not_verified", Code: "host_not_checked"},
		{ID: "talk.discovery", State: "warn", Code: "nextcloud_unreachable"},
	}
	sortReadinessRows(checks)
	suppressBlockedRows(checks)
	for _, c := range checks {
		if c.ID == "talk.discovery" && c.Code != "nextcloud_unreachable" {
			t.Fatalf("a finding was hidden behind an unchecked prerequisite: %+v", c)
		}
	}
}

// A blocked row must not be checkable either. The probe behind it exists, but
// running it cannot succeed while the prerequisite is unmet, so a Check button
// only invites a reader to press something that fails.
func TestABlockedRowIsNotCheckable(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checks = []readinessCheck{
		{ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled"},
		{ID: "talk.discovery", State: "needs_action", Code: "recording_auth_rejected", Action: "recheck"},
	}
	rt.recordingSetup.checkedAt = time.Now()
	rt.recordingSetup.mu.Unlock()

	for _, c := range rt.readiness(context.Background()).Checks {
		if c.ID != "talk.discovery" {
			continue
		}
		if c.Code != "check_blocked" {
			t.Fatalf("talk.discovery = %+v; want it blocked", c)
		}
		if c.Checkable {
			t.Error("a blocked row says it can be re-checked on its own")
		}
		return
	}
	t.Fatal("talk.discovery missing")
}

// The operator reports the credential row in every state, including configured.
//
// The panel used to invent this row whenever the operator omitted it, choosing
// its state, message, action, position and its own suppression rule — and
// drifted, carrying wording this function had already replaced.
// A saved credential must stay editable from the row it belongs to. Without
// this the form is unreachable the moment the check passes, and the value can
// never be rotated from the panel again.
//
// What is NOT here any more: a row reading "Passed" because a value was saved.
// Saving proves only that something was typed; this row reports whether the
// backend accepted it, which is a different claim and the one worth making.
func TestASavedCredentialStaysEditableOnTheBackendRow(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	putRecordingSetup(t, rt, `{"internal_secret":"a-saved-secret"}`, 200)
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checks = []readinessCheck{{ID: "talk.hpb", State: "passed", Code: "hpb_authenticated", Message: "accepted"}}
	rt.recordingSetup.checkedAt = time.Now()
	rt.recordingSetup.mu.Unlock()

	var row *readinessCheck
	for _, c := range rt.readiness(context.Background()).Checks {
		if c.ID == "talk.hpb" {
			copy := c
			row = &copy
		}
		if c.ID == "talk.authentication" {
			t.Fatal("a separate credential row is back; it reported one fact twice")
		}
	}
	if row == nil {
		t.Fatal("no backend row")
	}
	if row.State != "passed" || row.Code != "hpb_authenticated" {
		t.Fatalf("a saved secret rewrote a verdict somebody checked: %+v", row)
	}
	if row.Action != "configure_talk" {
		t.Errorf("the credential form is unreachable once the check passes: %+v", row)
	}
}

// The test recording tool, which shipped unreachable: it refused to run until a
// test room was chosen, and the row offering that form sat behind checks that
// needed the room themselves. The room is Cassini's to create now, and the row
// appears only where a test could actually succeed.
func TestTestRecordingRowWaitsForTheChainItExercises(t *testing.T) {
	checks := []readinessCheck{
		{ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled"},
		{ID: "storage", State: "passed", Code: "storage_ready"},
		{ID: "test", State: "not_verified", Code: "test_not_run", Action: "test_recording"},
	}
	sortReadinessRows(checks)
	suppressBlockedRows(checks)
	for _, c := range checks {
		if c.ID != "test" {
			continue
		}
		if c.Code != "check_blocked" {
			t.Fatalf("test = %+v; a test cannot run without a backend to record through", c)
		}
		// The button is what makes the tool reachable, so a blocked row keeping
		// it is how an administrator gets invited to run a test that must fail.
		if c.Action != "" {
			t.Errorf("a blocked test row still offers %q", c.Action)
		}
		if !strings.Contains(c.Message, "High Performance Backend") {
			t.Errorf("the blocked test row does not name its blocker: %q", c.Message)
		}
	}
}

func TestTestRecordingRowReportsWhatTheTestEstablished(t *testing.T) {
	rt := &Runtime{}
	for _, tc := range []struct {
		name  string
		test  readinessTest
		state string
		code  string
	}{
		{"nothing run", readinessTest{State: "not_started"}, "not_verified", "test_not_run"},
		{"armed", readinessTest{StartedAt: "2026-10-02T10:00:00Z", State: "waiting_for_talk"}, "not_verified", "test_in_progress"},
		{"published", readinessTest{StartedAt: "2026-10-02T10:00:00Z", JobID: "j1", Published: true, State: "succeeded"}, "not_verified", "test_awaiting_playback"},
		{"played back", readinessTest{StartedAt: "2026-10-02T10:00:00Z", JobID: "j1", Published: true, State: "succeeded", PlaybackVerifiedAt: "2026-10-02T10:05:00Z"}, "passed", "test_playback"},
		{"failed", readinessTest{StartedAt: "2026-10-02T10:00:00Z", JobID: "j1", State: "failed", Stage: "upload"}, "needs_action", "test_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rt.testRecordingRow(tc.test)
			if row.State != tc.state || row.Code != tc.code {
				t.Fatalf("row = %s/%s; want %s/%s", row.State, row.Code, tc.state, tc.code)
			}
			// Evidence stays dated: a playback confirmed months ago is still
			// true, and still months old.
			if tc.code == "test_playback" && row.CheckedAt != tc.test.PlaybackVerifiedAt {
				t.Errorf("playback row is undated: %+v", row)
			}
		})
	}
}

// A tool offering evidence on request is not a fault. Counting a test nobody
// ran would leave a fully passing install permanently reading "needs
// verification", with nothing an administrator could do to clear it.
func TestAnUnrunTestDoesNotLowerTheVerdict(t *testing.T) {
	passing := []readinessCheck{
		{ID: "storage", State: "passed", Code: "storage_ready"},
		{ID: "test", State: "not_verified", Code: "test_not_run"},
	}
	if got := worstReadinessState(verdictRows(passing)); got != "passed" {
		t.Fatalf("verdict = %q; an unrun test must not unverify a passing install", got)
	}
	// A test that FAILED is a finding, and counts.
	failed := []readinessCheck{
		{ID: "storage", State: "passed", Code: "storage_ready"},
		{ID: "test", State: "needs_action", Code: "test_failed"},
	}
	if got := worstReadinessState(verdictRows(failed)); got != "needs_action" {
		t.Fatalf("verdict = %q; a failed test recording is a real fault", got)
	}
}

// Every row a message can name has to be named the way the panel labels it: a
// message calling a row something the reader cannot see on screen sends them
// hunting for it.
func TestBlockerNamesMatchTheRowsAPanelShows(t *testing.T) {
	for id := range readinessPrerequisites {
		for _, prereq := range readinessPrerequisites[id] {
			if readinessRowNames[prereq] == "" {
				t.Errorf("%s blocks %s but has no name to be called by", prereq, id)
			}
		}
	}
}

// A conversation Cassini created can be deleted. Nothing about the stored URL
// changes when it is: it still parses, so without this the connection check
// fails forever against a room that is gone, with nothing a reader can do.
func TestAMissingRoomIsForgottenSoTheNextCheckRemakesIt(t *testing.T) {
	if !roomIsGone([]readinessCheck{{ID: "talk.discovery", Code: "talk_or_room_unavailable"}}) {
		t.Error("a 404 from Talk's recording settings must retire the stored room")
	}
	if !roomIsGone([]readinessCheck{{ID: "talk.discovery", Code: "test_room_invalid"}}) {
		t.Error("an unresolvable room must be retired")
	}
	// Everything else leaves it alone: a rejected credential or an unreachable
	// Nextcloud says nothing about whether the conversation exists, and
	// discarding it there would make every check create another one.
	for _, code := range []string{"recording_auth_rejected", "nextcloud_unreachable", "recording_auth_verified", "probe_failed"} {
		if roomIsGone([]readinessCheck{{ID: "talk.discovery", Code: code}}) {
			t.Errorf("%s retired the test room; only a missing room should", code)
		}
	}
}

// The test recording is an invitation, not a finding, so an unproven
// prerequisite must also hold it back. Observed on a stack with no High
// Performance Backend: the talk probe had not yet produced its row, nothing was
// there for suppression to consult, and the panel offered "Record a test" on an
// installation that cannot record at all.
func TestTheTestWaitsForEvidenceNotJustForTheAbsenceOfFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  []readinessCheck
		block bool
		says  string
	}{
		{"no hpb row at all",
			[]readinessCheck{{ID: "storage", State: "passed"}, {ID: "talk.discovery", State: "passed"}},
			true, "has not been checked yet"},
		{"hpb never checked",
			[]readinessCheck{{ID: "storage", State: "passed"}, {ID: "talk.hpb", State: "not_verified", Code: "host_not_checked"}, {ID: "talk.discovery", State: "passed"}},
			true, "has not been checked yet"},
		{"connection never checked",
			[]readinessCheck{{ID: "storage", State: "passed"}, {ID: "talk.hpb", State: "passed"}, {ID: "talk.discovery", State: "not_verified", Code: "connection_not_checked"}},
			true, "has not been checked yet"},
		{"hpb failed",
			[]readinessCheck{{ID: "storage", State: "passed"}, {ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled"}, {ID: "talk.discovery", State: "passed"}},
			true, "needs attention first"},
		{"everything proven",
			[]readinessCheck{{ID: "storage", State: "passed"}, {ID: "talk.hpb", State: "passed"}, {ID: "talk.discovery", State: "passed"}},
			false, ""},
		// A warning is a verdict: impaired but working, so a test can still run
		// and is worth running.
		{"a warning still allows the test",
			[]readinessCheck{{ID: "storage", State: "passed"}, {ID: "talk.hpb", State: "warn", Code: "signaling_unreachable"}, {ID: "talk.discovery", State: "warn"}},
			false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := append(tc.rows, readinessCheck{ID: "test", State: "not_verified", Code: "test_not_run", Action: "test_recording"})
			sortReadinessRows(checks)
			suppressBlockedRows(checks)
			var test readinessCheck
			for _, c := range checks {
				if c.ID == "test" {
					test = c
				}
			}
			if tc.block {
				if test.Code != "check_blocked" {
					t.Fatalf("test = %+v; want it to wait", test)
				}
				if test.Action != "" {
					t.Errorf("a waiting test row still offers %q", test.Action)
				}
				if !strings.Contains(test.Message, tc.says) {
					t.Errorf("message %q does not say %q", test.Message, tc.says)
				}
				return
			}
			if test.Code != "test_not_run" || test.Action != "test_recording" {
				t.Fatalf("test = %+v; want it offered", test)
			}
		})
	}
}

// Talk's Start recording action belongs to a conversation's moderators, and the
// creator of a conversation is its owner. A room created by Cassini's own
// account therefore leaves the administrator who opens it an ordinary
// participant, unable to start the recording they were just told to start.
// Reported from staging: "I pressed Prepare test and opened the room, but I
// can't start a recording since I'm an admin but not a room moderator."
func TestArmingATestMakesTheRoomBelongToWhoeverArmedIt(t *testing.T) {
	rt := &Runtime{}
	rt.cfg.TalkBackendURL = "https://nc.test"
	for _, tc := range []struct {
		name     string
		existing string
		owner    string
		want     string
		remake   bool
	}{
		{"no room yet", "", "", "admin", true},
		{"somebody else's room", "https://nc.test/call/abc12345", "cassini", "admin", true},
		{"already theirs", "https://nc.test/call/abc12345", "admin", "admin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The decision the handler makes before it calls out to Talk.
			remake := !rt.validTestRoom(tc.existing) || (tc.want != "" && tc.owner != tc.want)
			if remake != tc.remake {
				t.Fatalf("remake = %v, want %v: a room the administrator cannot moderate is no use to them", remake, tc.remake)
			}
		})
	}
}

// The connection check only needs a token to read Talk's recording settings
// with, so it must reuse whatever room exists rather than making a second one
// every time a different administrator has armed a test.
func TestTheConnectionCheckAcceptsAnyExistingRoom(t *testing.T) {
	rt := &Runtime{}
	rt.cfg.TalkBackendURL = "https://nc.test"
	room := "https://nc.test/call/abc12345"
	got, err := rt.ensureTestRoom(context.Background(), room, "admin", "")
	if err != nil {
		t.Fatalf("ensureTestRoom: %v", err)
	}
	if got != room {
		t.Fatalf("got %q; the check must not replace a usable room it did not create", got)
	}
}

// A caller who names a room has chosen it deliberately. The installed-ExApp
// e2e arms the test against the private conversation it is about to record in,
// and then asserts the test followed THAT job to publication — so silently
// substituting a freshly created room would not fail loudly, it would measure
// the wrong thing. Creating one as the acting administrator is for the panel's
// button, which names no room.
func TestArmingHonoursARoomTheCallerNamed(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	rt.cfg.TalkBackendURL = "https://nc.test"
	named := "https://nc.test/index.php/call/chosen12"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/talk/setup",
		strings.NewReader(`{"test_room_url":"`+named+`","action":"arm_test"}`))
	// The header AppAPI signs every proxied request with; without the fix the
	// acting user is what triggers the substitution.
	req.Header.Set("AUTHORIZATION-APP-API",
		base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	rt.recordingSetupHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("arm_test = %d: %s", rec.Code, rec.Body.String())
	}
	var resp readinessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TestRoomURL != named {
		t.Fatalf("test room = %q, want the room the caller named (%q)", resp.TestRoomURL, named)
	}
	if resp.Test.StartedAt == "" {
		t.Error("the test was not armed")
	}
}
