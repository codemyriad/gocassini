package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var errRecordingSetup = errors.New("recording setup incomplete")

const readinessTTL = 5 * time.Minute

type readinessCheck struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Action is a verb the panel renders as a button. It answers "where do I go
	// to fix this", and only inside the app.
	Action string `json:"action,omitempty"`
	// Steps are the remedy for a check that is not ok, in words (D-798 R0.1).
	// Where the operator can perform the remedy, Repair below carries it and the
	// panel offers a button instead.
	//
	// Action cannot carry this. Plenty of remedies are not a place to navigate
	// to — they are a command to run on a host the app cannot reach, or a
	// sentence naming what to look at. Leaving those in the message told a
	// reader what was wrong and not what to do, which is the failure the
	// existing SetupNotice was built to avoid for storage faults.
	Steps []readinessStep `json:"steps,omitempty"`
	// Repair names something the operator can do about this check ITSELF, which
	// the panel renders as a button, instead of printing a shell line for an
	// administrator to go and run.
	Repair string `json:"repair,omitempty"`
	// Checkable says a probe establishes THIS row, so it can be re-checked on
	// its own. Sent rather than worked out again in the panel: the mapping from
	// row to probe is readinessScopeFor's, and a second copy in TypeScript
	// drifts into a spinner for a probe that never runs, or a button the server
	// refuses.
	Checkable bool `json:"checkable,omitempty"`
	// Docs is where to read about this check, for a fault the operator cannot
	// repair and should not pretend to instruct. A stable URL, not a procedure.
	Docs      string `json:"docs,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// readinessStep mirrors SetupNoticeStep, deliberately: the app already renders
// that shape for storage faults, with the commands behind a disclosure so an
// administrator who just wants the button never reads a command line.
type readinessStep struct {
	Label string `json:"label"`
}

type recordingSetupState struct {
	InternalSecret     string `json:"internal_secret,omitempty"`
	TestRoomURL        string `json:"test_room_url,omitempty"`
	TestStartedAt      string `json:"test_started_at,omitempty"`
	PlaybackJobID      string `json:"playback_job_id,omitempty"`
	PlaybackVerifiedAt string `json:"playback_verified_at,omitempty"`
}

type recordingSetup struct {
	mu         sync.Mutex
	checkMu    sync.Mutex
	loaded     bool
	loadFailed bool
	state      recordingSetupState
	checks     []readinessCheck
	checkedAt  time.Time
	hostChecks []readinessCheck
	// probedAt is when each scope last ran, for coalescing duplicate clicks.
	probedAt  map[string]time.Time
	inboundAt time.Time
	probe     func(context.Context, string) ([]readinessCheck, error)
}

type readinessTest struct {
	StartedAt          string `json:"started_at,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	Stage              string `json:"stage,omitempty"`
	State              string `json:"state"`
	Published          bool   `json:"published"`
	PlaybackVerifiedAt string `json:"playback_verified_at,omitempty"`
	ViewerURL          string `json:"viewer_url,omitempty"`
}

type readinessResponse struct {
	State string `json:"state"`
	// RecordingState excludes optional processing and archive findings.
	RecordingState   string           `json:"recording_state"`
	Checks           []readinessCheck `json:"checks"`
	SecretConfigured bool             `json:"secret_configured"`
	SecretSource     string           `json:"secret_source"`
	TestRoomURL      string           `json:"test_room_url"`
	Test             readinessTest    `json:"test"`
}

func (rt *Runtime) recordingSetupPath() string {
	return filepath.Join(filepath.Dir(rt.cfg.DBPath), "recording-setup.json")
}

// Caller holds mu. A corrupt store remains an actionable failure, never a
// silent reset of credentials or evidence.
func (rt *Runtime) loadRecordingSetupLocked() {
	s := &rt.recordingSetup
	if s.loaded {
		return
	}
	s.loaded = true
	raw, err := os.ReadFile(rt.recordingSetupPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || json.Unmarshal(raw, &s.state) != nil {
		s.loadFailed = true
		s.state = recordingSetupState{}
	}
}

func (rt *Runtime) saveRecordingSetupLocked(next recordingSetupState) error {
	path := rt.recordingSetupPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".recording-setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	rt.recordingSetup.state = next
	rt.recordingSetup.loadFailed = false
	return nil
}

func (rt *Runtime) signalingSecret() (string, string) {
	if secret := strings.TrimSpace(os.Getenv(envTalkSignalingInternalSecret)); secret != "" {
		return secret, "env"
	}
	rt.recordingSetup.mu.Lock()
	defer rt.recordingSetup.mu.Unlock()
	rt.loadRecordingSetupLocked()
	if rt.recordingSetup.state.InternalSecret != "" {
		return rt.recordingSetup.state.InternalSecret, "setup"
	}
	return "", "unset"
}

// The pasted URL supplies the public identity for HPB. OCS probes use the deployment's
// trusted backend, never a host supplied through this API. This supports public
// URLs when AppAPI uses an internal address. The pasted host is never dialed.
func (rt *Runtime) readinessBackendURL() string {
	base := strings.TrimSpace(rt.cfg.TalkBackendURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv(envNextcloudURL))
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return strings.TrimRight(base, "/")
}

func testRoomToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "call" {
		return ""
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	token := parts[len(parts)-1]
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return token
}

// appAPIProxyPath is how AppAPI addresses an ExApp through Nextcloud. A URL
// containing it points at Cassini, never at Talk.
const appAPIProxyPath = "/apps/app_api/proxy/"

// talkBackendMisconfigured reports why the configured Talk backend URL cannot
// be one, or "" when it is fine.
//
// CASSINI_TALK_BACKEND_URL is the base the operator calls Talk on. Talk's OWN
// recording configuration needs the opposite direction — the AppAPI proxy URL
// where Talk reaches Cassini — and the two are easy to swap, because both are
// "the Talk backend URL" in conversation.
//
// Swapped, every Talk request goes through the proxy to Cassini itself,
// Nextcloud answers with an HTML 404, nothing parses as OCS, and the probe
// reports "Could not read Talk settings. Check Nextcloud connectivity and TLS"
// — a true sentence whose remedy is a dead end, because connectivity and TLS
// are fine and the request was simply sent to the wrong service. Diagnosed once
// on the demo, from the outside, at the cost of an afternoon.
//
// A path alone is not the tell: Nextcloud legitimately lives under a
// subdirectory. Addressing an ExApp through AppAPI is.
func (rt *Runtime) talkBackendMisconfigured() string {
	raw := strings.TrimSpace(rt.cfg.TalkBackendURL)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && strings.Contains(u.Path, appAPIProxyPath) {
		return "Cassini's configured Talk backend URL addresses Cassini's own AppAPI proxy, so requests for Talk's settings never reach Talk. Clear CASSINI_TALK_BACKEND_URL unless a deployment genuinely needs an override; Talk sends the correct backend URL with each recording request."
	}
	if rt.readinessBackendURL() == "" {
		return "Cassini's configured Talk backend URL is not a usable base URL. It must be a plain http(s) URL with no query or fragment. Clear CASSINI_TALK_BACKEND_URL unless a deployment genuinely needs an override."
	}
	return ""
}

func (rt *Runtime) validTestRoom(raw string) bool {
	return rt.readinessBackendURL() != "" && testRoomToken(raw) != ""
}

func (rt *Runtime) connectionProbeArgs(room string) ([]string, error) {
	base, token := rt.readinessBackendURL(), testRoomToken(room)
	if base == "" || token == "" {
		return nil, errors.New("invalid diagnostic target")
	}
	return []string{"talk-check", "--call", room, "--connect-url", base}, nil
}

func (rt *Runtime) runConnectionProbe(ctx context.Context, room string) ([]readinessCheck, error) {
	args, err := rt.connectionProbeArgs(room)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, args...)
	cmd.Env = rt.recordChildEnv()
	// Probe output is a small JSON document; upstream protocol logs stay out of
	// both responses and operator logs because they may contain private details.
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var checks []readinessCheck
	if err := json.Unmarshal(out, &checks); err != nil {
		return nil, err
	}
	if len(checks) == 0 {
		return nil, errors.New("empty probe")
	}
	for _, c := range checks {
		if readinessStateRank[c.State] == 0 && c.State != "passed" {
			// Every state the ladder knows, including warn — a probe that TRIED
			// and could not reach something has a finding, not an absence, and
			// rejecting warn here is what forced those outcomes to report
			// themselves as "not verified" (D-798).
			return nil, errors.New("invalid probe state")
		}
	}
	return checks, nil
}

// Host checks, from the recorder (D-798 V2).
//
// Mirrors runConnectionProbe deliberately: same boundary, same discipline —
// stderr discarded because upstream output is not ours to relay, states
// validated on entry, an empty document treated as an error rather than as "no
// problems found". The recorder's own host is the operator's host in the ExApp
// image, so these findings describe the machine this API is served from.
//
// doctor speaks ok/warn/fail. Mapped here at the edge:
//
//	ok    -> passed
//	warn  -> warn        the media host is impaired but still usable
//	fail  -> needs_action
//
// This target checks the media host needed for recording and publication.
// Optional speech-model readiness has its own processing row.
func (rt *Runtime) runDoctorProbe(ctx context.Context) ([]readinessCheck, error) {
	bin := strings.TrimSpace(rt.cfg.CassiniBin)
	if bin == "" {
		return nil, errors.New("no recorder binary configured")
	}
	cmd := exec.CommandContext(ctx, bin, "doctor", "--target", "media", "--json")
	cmd.WaitDelay = 500 * time.Millisecond
	// Doctor checks its working directory for writability and free space.
	// Use the recording volume, which can differ from the image's CWD.
	if rt.cfg.WorkRoot != "" {
		cmd.Dir = filepath.Dir(rt.cfg.WorkRoot)
	}
	// Doctor checks media tools and disk space. It needs the recorder's runtime
	// configuration, but not AppAPI impersonation or Talk credentials.
	cmd.Env = withoutEnv(rt.childEnv(), operatorOnlySecretEnv())
	cmd.Stderr = io.Discard
	// A non-zero exit is how doctor reports `fail`, so the document is still
	// what matters — read it whenever there is one.
	out, err := cmd.Output()
	if len(out) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("empty doctor output")
	}
	var reported []struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Summary string `json:"summary"`
		Advice  string `json:"advice"`
	}
	if err := json.Unmarshal(out, &reported); err != nil {
		return nil, err
	}
	if len(reported) == 0 {
		return nil, errors.New("doctor reported no checks")
	}
	byID := make(map[string]readinessCheck, len(reported))
	for _, r := range reported {
		state := ""
		switch r.Status {
		case "ok":
			state = "passed"
		case "warn":
			state = "warn"
		case "fail":
			state = "needs_action"
		default:
			// A doctor this operator does not understand is not evidence that
			// the host is healthy.
			return nil, fmt.Errorf("unknown doctor status %q", r.Status)
		}
		if strings.TrimSpace(r.ID) == "" {
			return nil, errors.New("doctor check with no id")
		}
		check := readinessCheck{
			ID:      "host." + r.ID,
			State:   state,
			Code:    r.ID,
			Message: r.Summary,
		}
		// doctor's advice is already the remedy in prose. It becomes a step
		// rather than being appended to the summary, so the message stays the
		// finding and the step stays the fix.
		if state != "passed" && strings.TrimSpace(r.Advice) != "" {
			check.Steps = []readinessStep{{Label: r.Advice}}
		}
		byID[r.ID] = check
	}
	return hostChecklistRows(byID), nil
}

// hostChecklistRows is the small set the checklist shows, out of everything
// doctor reports.
//
// doctor keeps all of it: it is a standalone host diagnostic and its text is a
// shipped format, and ffmpeg or a filling disk is exactly what someone wants
// from a terminal. The panel is a different audience with a different question
// — "is there something here I can act on" — and free space, ffprobe and the
// rest answered it with rows nobody ever acted on.
//
// Surfacing them only when they warn or fail was tried and reverted: a row that
// appears exactly when an administrator can do nothing useful about it is the
// version of this the review removed, not a milder one. If a filling disk should
// raise something, it should raise a row that says what to do about it — not
// ffprobe's presence, reported to somebody who has never heard of ffprobe.
//
// workdir and workdir.writable are one fact to a reader: whether Cassini can
// use its recording volume. They are reported separately because they fail for
// different reasons, which matters to doctor and not to this list, so they
// collapse into one row carrying the worse of the two.
// hostChecklistRowIDs is the host rows the panel shows, in order. Declared once
// so the unchecked placeholder and the probed result cannot disagree about which
// rows exist.
var hostChecklistRowIDs = []string{"host.workdir", "host.tmpdir.writable"}

func hostChecklistRows(byID map[string]readinessCheck) []readinessCheck {
	var rows []readinessCheck
	if row, ok := worseOf(byID["workdir"], byID["workdir.writable"]); ok {
		row.ID, row.Code = "host.workdir", "workdir"
		rows = append(rows, row)
	}
	if row, ok := byID["tmpdir.writable"]; ok {
		rows = append(rows, row)
	}
	return rows
}

// worseOf returns whichever check reports the worse news, so a collapsed row
// never reads better than its worst half. An absent check is not evidence of
// health, so it simply yields to the one that is present.
func worseOf(a, b readinessCheck) (readinessCheck, bool) {
	switch {
	case a.ID == "" && b.ID == "":
		return readinessCheck{}, false
	case a.ID == "":
		return b, true
	case b.ID == "":
		return a, true
	}
	if readinessStateRank[b.State] > readinessStateRank[a.State] {
		return b, true
	}
	return a, true
}

// Coalesce concurrent checks and put a ceiling on network/process work. GET
// reports cached host and connection findings; only startup and explicit
// checks launch these probes. Aged findings keep their verdict and timestamp.
func (rt *Runtime) checkRecordingReadiness(ctx context.Context) {
	rt.checkRecordingReadinessScoped(ctx, allReadinessScopes())
}

// checkRecordingReadinessScoped runs the probes the scope names, and only those.
//
// Coalesce concurrent checks and put a ceiling on network/process work. GET
// reports cached findings; only an explicit check launches these probes. Aged
// findings keep their verdict and timestamp.
//
// Each probe is coalesced under its own name rather than one window over the
// whole run, so retrying the host does not silently skip a storage check
// somebody asked for a second later.
func (rt *Runtime) checkRecordingReadinessScoped(ctx context.Context, scope readinessScope) {
	if scope.empty() {
		return
	}
	s := &rt.recordingSetup
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	rt.loadRecordingSetupLocked()
	room, probe := s.state.TestRoomURL, s.probe
	s.mu.Unlock()
	if probe == nil {
		probe = rt.runConnectionProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	started := time.Now()

	if scope.storage && s.beginProbe("storage", started) {
		if cfg, err := LoadExAppConfig(); err == nil && cfg.Active {
			cfg.preflightDirectShares(ctx, rt.logger)
		}
	}

	if scope.host && s.beginProbe("host", started) {
		// Probe the media host once per explicit check and retain its verdict
		// with the time it was checked.
		hostCtx, hostCancel := context.WithTimeout(ctx, 3*time.Second)
		host, hostErr := rt.runDoctorProbe(hostCtx)
		hostCancel()
		hostAt := time.Now().UTC().Format(time.RFC3339)
		if hostErr != nil {
			host = []readinessCheck{{
				ID: "host", State: "warn", Code: "host_checks_unavailable",
				Message: "Cassini could not check disk space and ffmpeg on the recording volume.",
				Action:  "recheck",
				Steps:   []readinessStep{{Label: "Check the recorder's media tools and its recording volume, then run the checks again"}},
			}}
		}
		for i := range host {
			host[i].CheckedAt = hostAt
		}
		s.mu.Lock()
		s.hostChecks = host
		s.mu.Unlock()
	}

	if scope.talk && s.beginProbe("talk", started) {
		var checks []readinessCheck
		var roomErr error
		if strings.TrimSpace(rt.cfg.TalkSharedSecret) != "" && rt.talkBackendMisconfigured() == "" && !rt.validTestRoom(room) {
			// The connection check needs a room token. It used to refuse until
			// somebody pasted a room URL into a form, which is the one piece of
			// configuration Cassini can do for itself. Created once and kept,
			// so the test recording below reuses the same conversation.
			if created, err := rt.ensureTestRoom(ctx, room); err == nil {
				s.mu.Lock()
				next := s.state
				next.TestRoomURL = created
				if err := rt.saveRecordingSetupLocked(next); err != nil {
					rt.logger.Printf("ERROR: could not persist the created test room: %v", err)
				}
				s.mu.Unlock()
				room = created
			} else {
				roomErr = err
				rt.logger.Printf("ERROR: could not create a test room for the connection check: %v", err)
			}
		}
		if roomErr != nil {
			checks = append(checks, readinessCheck{
				ID: "talk.discovery", State: "needs_action", Code: "test_room_unavailable",
				Message: "Cassini could not create the conversation it checks the connection with. Check that Talk is installed and that Cassini's account may create conversations.",
				Steps:   []readinessStep{{Label: "Confirm the Talk app is enabled, then run this check again"}},
			})
		} else if strings.TrimSpace(rt.cfg.TalkSharedSecret) != "" && rt.validTestRoom(room) && rt.talkBackendMisconfigured() == "" {
			var err error
			checks, err = probe(ctx, room)
			if err != nil {
				checks = []readinessCheck{{ID: "talk.discovery", State: "not_verified", Code: "probe_failed", Message: "The connection check did not finish. Check the recorder installation and connectivity, then retry.", Action: "recheck"}}
			}
		}
		// Talk's own signaling mode, read from capabilities. The probe learns the
		// same fact from Talk's recording settings, which needs the recording
		// credential AND a test room — so without this, a deployment failing
		// either never discovers it has no HPB, the one fault that stops
		// recording outright. Appended only when the probe did not get far
		// enough to report it itself.
		if hpb := rt.hpbFinding(ctx); hpb != nil {
			found := false
			for _, c := range checks {
				if c.ID == "talk.hpb" {
					found = true
				}
			}
			if !found {
				checks = append(checks, *hpb)
			}
		}
		// A room Cassini made can be deleted — by an administrator tidying up,
		// or with the whole Talk database. Nothing about the stored URL changes
		// when that happens: it still parses, so validTestRoom still accepts
		// it, and every future check fails against a conversation that is gone
		// with no way back. Forgetting it is what lets the next check make a
		// new one.
		if roomIsGone(checks) {
			s.mu.Lock()
			next := s.state
			next.TestRoomURL = ""
			if err := rt.saveRecordingSetupLocked(next); err != nil {
				rt.logger.Printf("ERROR: could not forget the missing test room: %v", err)
			}
			s.mu.Unlock()
			room = ""
		}
		now := time.Now().UTC()
		for i := range checks {
			checks[i].CheckedAt = now.Format(time.RFC3339)
		}
		s.mu.Lock()
		// A configuration edit while the probe was running invalidates its
		// answer. `room` is re-read above when this probe created one, so a
		// self-created room does not look like somebody else's edit.
		if s.state.TestRoomURL == room {
			s.checks = checks
			s.checkedAt = now
		}
		s.mu.Unlock()
	}

	if scope.archive && s.beginProbe("archive", started) {
		// Listing the archive is a PROPFIND and reading the index is
		// O(archive + index), so it belongs behind an explicit check rather
		// than on the route the panel loads to render.
		rt.recordArchiveCoverage(ctx)
	}
}

func (rt *Runtime) readiness(ctx context.Context) readinessResponse {
	return rt.readinessWithOptional(ctx, true)
}

// The public setup route needs only RecordingState. Optional transcription and
// archive coverage cannot affect that verdict, so ordinary users do not run
// those diagnostics whenever they open the app.
func (rt *Runtime) readinessWithOptional(ctx context.Context, includeOptional bool) readinessResponse {
	secret, source := rt.signalingSecret()
	s := &rt.recordingSetup
	s.mu.Lock()
	rt.loadRecordingSetupLocked()
	state, failed, checkedAt := s.state, s.loadFailed, s.checkedAt
	probes := append([]readinessCheck(nil), s.checks...)
	host := append([]readinessCheck(nil), s.hostChecks...)
	s.mu.Unlock()
	resp := readinessResponse{State: "passed", Checks: []readinessCheck{}, SecretConfigured: secret != "", SecretSource: source, TestRoomURL: state.TestRoomURL}
	add := func(id, state, code, message, action string) {
		resp.Checks = append(resp.Checks, readinessCheck{ID: id, State: state, Code: code, Message: message, Action: action})
	}
	// addWithSteps is for a remedy the app cannot perform on the reader's
	// behalf: a command on a host it cannot reach, or a thing to go and look at.
	addWithSteps := func(id, state, code, message, action string, steps ...readinessStep) {
		resp.Checks = append(resp.Checks, readinessCheck{
			ID: id, State: state, Code: code, Message: message, Action: action, Steps: steps,
		})
	}
	if failed {
		addWithSteps("configuration", "needs_action", "setup_store_unreadable",
			"Cassini could not read its saved recording setup, so its Talk credentials cannot be confirmed.",
			"repair_configuration",
			readinessStep{Label: "Check that Cassini's persistent volume is mounted and writable, then restore recording-setup.json from a backup if it is missing"},
			readinessStep{Label: "Disable and re-enable Cassini in Nextcloud, which re-runs its setup"})
	}
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	// Parsed only to tell "a check has run" from "none has". How OLD it is rides
	// on CheckedAt in the response, not on a branch here.
	_, storageTimeErr := time.Parse(time.RFC3339, access.CheckedAt)
	if !access.Applicable {
		add("storage", "not_verified", "storage_not_probed", "The Nextcloud storage check does not apply to this publish destination. Verify storage with a test recording.", "test_recording")
	} else if ncAccessSubstrate.recordingRefusal() != "" {
		add("storage", "needs_action", "storage_admission_blocked", "Cassini currently blocks recording on its stored storage status. Review the storage details below and check again after repairing them.", "setup_storage")
	} else if storageTimeErr != nil {
		// No parseable timestamp means no storage check has ever run here — an
		// absence, not a verdict (D-798). Age is a separate matter: the branches
		// below report what the last check FOUND and carry CheckedAt so a reader
		// can see how old it is. Expiring a passing check into "not verified"
		// made the resting state of an idle panel indistinguishable from a
		// problem, because nothing re-probes on its own (readiness is a read;
		// only checkRecordingReadiness probes).
		//
		// Safe because this panel reports rather than authorises: admission is
		// decided separately by ncAccessSubstrate.recordingRefusal(), checked
		// above and not bounded by this TTL.
		add("storage", "not_verified", "storage_not_checked", "Nextcloud storage has not been checked yet. Check again to run it.", "recheck")
	} else if access.OK {
		// Says what was established, in the reader's terms. "The Nextcloud
		// storage preflight passed" named an internal routine and no fact: the
		// row is green, and a reader still cannot tell what is now known to
		// work. What this check actually proves is the publish destination —
		// the account exists, its recordings folder was created over WebDAV, and
		// the sharing API answers for it — so say that.
		//
		// It no longer ends with "a test recording verifies publication and
		// playback" either. That sentence was this row apologising for what it
		// could not establish, back when no test recording could be run at all.
		// The Test recording row says it now, where a reader can act on it.
		resp.Checks = append(resp.Checks, readinessCheck{ID: "storage", State: "passed", Code: "storage_ready", Message: "Cassini can store and share recordings in Nextcloud: its own account exists, its recordings folder is writable, and Nextcloud's sharing API answers.", CheckedAt: access.CheckedAt})
	} else {
		// Not "below": the button leaves this panel for the Storage section, so
		// a reader told to look down the page looks in the wrong place.
		resp.Checks = append(resp.Checks, readinessCheck{ID: "storage", State: "needs_action", Code: "storage_incomplete", Message: "Cassini cannot store or share recordings in Nextcloud yet. The storage details name the step that failed.", Action: "setup_storage", CheckedAt: access.CheckedAt})
	}
	hpbDisabled := false
	for _, p := range probes {
		if p.Code == "hpb_disabled" {
			hpbDisabled = true
		}
	}
	if secret == "" && !failed {
		// Where to READ it, named. The same two locations the startup log has
		// always given — a row that asks for a secret and does not say where it
		// lives sends an administrator hunting, which is what happened.
		//
		// Two named places, not a procedure branched on a declared install type:
		// these are where the value IS, whoever deployed it.
		if hpbDisabled {
			// The secret authenticates Cassini to the High Performance Backend.
			// With no backend there is nothing to authenticate to, and nothing
			// an administrator can usefully do about this row yet — the backend
			// row above is the one to act on. Demanding it here reported one
			// fault twice and sent the reader to the wrong one.
			addWithSteps("talk.authentication", "not_verified", "internal_secret_not_needed_yet",
				"Not needed yet. Cassini joins a call as an invisible signaling client, and this secret is how it authenticates to Talk's High Performance Backend — it belongs to that server, not to Nextcloud. There is no backend configured, so there is nothing to authenticate to and setting it now would change nothing.",
				"",
				readinessStep{Label: "Declare a standalone signaling server under Talk → Administration settings → Signaling server. This credential becomes required, and checkable, once Talk names one"})
		} else {
			addWithSteps("talk.authentication", "needs_action", "internal_secret_missing",
				"This is not a Nextcloud setting. It belongs to Talk's signaling server, and Cassini needs the same value in order to join calls invisibly. Nextcloud does not hold it anywhere, which is why Cassini cannot read it for you.",
				"configure_talk",
				readinessStep{Label: "Nextcloud All-in-One: docker exec nextcloud-aio-talk printenv INTERNAL_SECRET"},
				readinessStep{Label: "Standalone signaling server: the `internalsecret` under `[clients]` in its configuration file"},
				readinessStep{Label: "Paste it unchanged — one differing character fails exactly as a wrong credential would, and nothing can tell the difference until the connection is checked"})
		}
	} else if !failed {
		// The configured case, which the OPERATOR now reports too.
		//
		// It used to be the panel's: when this row was absent the panel invented
		// one, choosing its state, its message, its action, its position, and
		// its own rule for when to suppress it. Five decisions this function
		// already makes for every other row, duplicated in TypeScript, and
		// drifted — the panel still carried the wording this row stopped using
		// days ago.
		//
		// "Configured", not "passed as verified": saving a secret proves it was
		// saved. Whether it is the RIGHT secret is the connection check's to
		// establish, and claiming more here is how a row reads green on an
		// install that cannot record.
		message := "An internal secret is saved. The connection check is what confirms Talk accepts it."
		if source == "env" {
			message = "The internal secret comes from Cassini's deployment configuration. The connection check is what confirms Talk accepts it."
		}
		add("talk.authentication", "passed", "internal_secret_configuration", message, "configure_talk")
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		add("talk.handoff", "needs_action", "recording_secret_missing", "Cassini could not provision its recording credential. Check its persistent storage.", "connect_talk")
	}
	if refusal := rt.talkBackendMisconfigured(); refusal != "" {
		// Ahead of the room check: no room can make this work, and "choose a
		// test room" would send a reader to fix the one thing that is fine.
		add("talk.discovery", "needs_action", "talk_backend_url_invalid", refusal, "")
	} else if len(probes) == 0 {
		// The message this replaces named both cases — "Previous results have
		// expired OR this process restarted" — while treating them as one. They
		// are different facts: no probe result at all is an absence, whereas an
		// aged one is a finding that happens to be old. Probe results are
		// in-memory only, so a restart genuinely leaves nothing established.
		add("talk.discovery", "not_verified", "connection_not_checked", "The Talk connection has not been checked yet. Check again to run it.", "recheck")
	} else {
		// Stamped with when the probe ran, so its age travels with the verdict
		// instead of replacing it.
		probedAt := checkedAt.UTC().Format(time.RFC3339)
		for _, probe := range probes {
			probe.CheckedAt = probedAt
			resp.Checks = append(resp.Checks, probe)
		}
	}
	// The backend row ALWAYS appears. It is the one check that decides whether
	// recording can work at all, and it was the only row with no way to say
	// "nobody has established this": every other row has one — host_not_checked,
	// storage_not_checked, connection_not_checked — while this one simply was
	// not rendered.
	//
	// So it disappeared, and did so at the worst times. Probe findings live in
	// memory only, so a restart leaves none until a check runs; each check
	// replaces the whole set, so one run that cannot establish the backend
	// erases what the last run knew; and the probe reports no backend row at all
	// when it stops earlier in the chain. A reader watching the most important
	// check come and go cannot tell which of those happened — and the rows that
	// depend on it quietly stopped waiting for it, because there was nothing
	// there to wait for.
	if !hasReadinessRow(resp.Checks, "talk.hpb") {
		add("talk.hpb", "not_verified", "hpb_not_checked",
			"Whether Talk has a High Performance Backend has not been established yet, and Cassini can only record through one. Check again to run it.",
			"recheck")
	}
	if includeOptional {
		resp.Checks = append(resp.Checks, rt.lastArchiveCoverage())
	}

	// Host findings lead so a full disk can explain a storage failure. GET
	// never launches the media doctor subprocess.
	if len(host) == 0 {
		// The SAME rows a check produces, unchecked — not one row standing in
		// for them. A placeholder with its own id renamed itself on the first
		// check: "Recording host" became "Recording volume" and "Temporary
		// space", which reads as a row changing identity rather than a verdict
		// arriving. A row keeps its name and changes its state; that is the
		// whole point of the ladder.
		host = make([]readinessCheck, 0, len(hostChecklistRowIDs))
		for _, id := range hostChecklistRowIDs {
			host = append(host, readinessCheck{
				ID: id, State: "not_verified", Code: "host_not_checked",
				Message: "Not checked yet.", Action: "recheck",
			})
		}
	}
	resp.Checks = append(host, resp.Checks...)
	resp.Test = rt.readinessTest(ctx, state)
	// Before suppression, so the test row waits on its chain like any other.
	resp.Checks = append(resp.Checks, rt.testRecordingRow(resp.Test))
	sortReadinessRows(resp.Checks)
	suppressBlockedRows(resp.Checks)
	for i := range resp.Checks {
		// A blocked row is not checkable. A probe behind it exists, but running
		// it cannot succeed while its prerequisite is unmet, so offering a
		// Check button invites a reader to press something that will fail.
		resp.Checks[i].Checkable = resp.Checks[i].Code != "check_blocked" &&
			!readinessScopeFor([]string{resp.Checks[i].ID}).empty()
	}
	resp.State = worstReadinessState(verdictRows(resp.Checks))
	resp.RecordingState = recordingCapabilityState(resp.Checks)
	return resp
}

// Optional processing and archive coverage deserve their own findings, but
// cannot turn a working audio recorder into a reported recording failure.
func recordingCapabilityState(checks []readinessCheck) string {
	core := make([]readinessCheck, 0, len(checks))
	for _, check := range checks {
		if check.ID == "processing" || strings.HasPrefix(check.ID, "archive.") {
			continue
		}
		// The test is evidence, not a dependency. An install whose every check
		// passes can record; that nobody has yet chosen to prove it by hand
		// must not report the instance as unverified.
		if check.ID == "test" {
			continue
		}
		core = append(core, check)
	}
	return worstReadinessState(core)
}

func hasReadinessRow(checks []readinessCheck, id string) bool {
	for _, check := range checks {
		if check.ID == id {
			return true
		}
	}
	return false
}

// verdictRows are the rows that may lower the instance's verdict.
//
// The test recording is evidence somebody chose to gather, so its ABSENCE says
// nothing about the instance: counting it would leave a fully passing install
// permanently reading "needs verification", with nothing an administrator could
// do about it short of recording something by hand. A test that actually FAILED
// is different — that is a finding, and it counts.
func verdictRows(checks []readinessCheck) []readinessCheck {
	rows := make([]readinessCheck, 0, len(checks))
	for _, check := range checks {
		if check.ID == "test" && check.State != "needs_action" {
			continue
		}
		rows = append(rows, check)
	}
	return rows
}

// worstReadinessState is the instance's worst news, ordered by how much it
// costs to ignore.
//
// `warn` sits between passed and needs_action: something is impaired but the
// thing still works, so it must neither be swallowed into "passed" nor promoted
// into a blocking failure. `not_verified` ranks below warn — nothing has been
// established, which is not the same as having found a problem.
// readinessRowOrder is the order the checklist reads in, declared rather than
// left to whichever order the assembling code happened to append in — which put
// the signaling credential ABOVE the backend it authenticates to, so a reader
// met the credential first and the reason it was not needed second.
//
// The talk rows are a dependency chain: a backend has to exist, then it needs
// its credential, then the connection can be verified. Host findings still lead,
// because a full disk explains a storage failure below it.
var readinessRowOrder = []string{
	"configuration",
	"host", "host.workdir", "host.tmpdir.writable",
	"storage",
	"talk.hpb",
	"talk.authentication",
	"talk.discovery",
	"talk.handoff",
	"test",
	"archive.search",
}

// sortReadinessRows orders in place, stably, leaving any id the list does not
// know at the end in the order it arrived — a new check appears rather than
// disappearing because nobody added it here.
func sortReadinessRows(checks []readinessCheck) {
	rank := func(id string) int {
		for i, known := range readinessRowOrder {
			if known == id {
				return i
			}
		}
		return len(readinessRowOrder)
	}
	sort.SliceStable(checks, func(i, j int) bool {
		return rank(checks[i].ID) < rank(checks[j].ID)
	})
}

// readinessStateRank orders the states by how much it costs to ignore them.
// One table, because two copies would eventually disagree about whether warn
// outranks not_verified.
var readinessStateRank = map[string]int{"passed": 0, "not_verified": 1, "warn": 2, "needs_action": 3}

func worstReadinessState(checks []readinessCheck) string {
	worst := "passed"
	rank := readinessStateRank
	for _, c := range checks {
		if rank[c.State] > rank[worst] {
			worst = c.State
		}
	}
	return worst
}

func (rt *Runtime) readinessTest(ctx context.Context, setup recordingSetupState) readinessTest {
	result := readinessTest{StartedAt: setup.TestStartedAt, State: "not_started"}
	if setup.TestStartedAt == "" || rt.store == nil {
		return result
	}
	result.State = "waiting_for_talk"
	u, _ := url.Parse(setup.TestRoomURL)
	if u == nil {
		return result
	}
	token := filepath.Base(u.Path)
	var id string
	// Only jobs started through Talk count. An operator-created job bypasses
	// the very handoff this exercise verifies. Pin the first job after arming.
	err := rt.store.db.QueryRowContext(ctx, `SELECT id FROM jobs WHERE room_token=? AND talk_binding IS NOT NULL AND created_at>=? ORDER BY created_at,id LIMIT 1`, token, setup.TestStartedAt).Scan(&id)
	if err != nil {
		return result
	}
	job, err := rt.store.GetJob(ctx, id)
	if err != nil {
		return result
	}
	result.JobID, result.Stage, result.State = id, job.Stage, job.State
	result.Published = job.State == "succeeded" && job.PublishFinishedAt != nil && job.CompletedAt != nil
	if result.Published {
		// Browse is an authenticated application route, never the local artifact path.
		result.ViewerURL = "#meeting=" + url.QueryEscape(id)
		if setup.PlaybackJobID == id {
			result.PlaybackVerifiedAt = setup.PlaybackVerifiedAt
		}
	}
	return result
}

func (rt *Runtime) readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/health" && r.Method == http.MethodGet:
	case r.URL.Path == "/health/check" && r.Method == http.MethodPost:
		// No body, or no `only`, runs every probe — what "Run all checks" sends.
		// A body naming rows runs just the probes behind them, so retrying one
		// row costs one probe instead of a whole-archive PROPFIND and a Talk
		// round trip.
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		if readErr != nil {
			writeJSONError(w, http.StatusBadRequest, "unreadable check request")
			return
		}
		scope := allReadinessScopes()
		if len(bytes.TrimSpace(raw)) > 0 {
			var body struct {
				Only []string `json:"only"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				writeJSONError(w, http.StatusBadRequest, "unreadable check request")
				return
			}
			if len(body.Only) > 0 {
				scope = readinessScopeFor(body.Only)
				if scope.empty() {
					// Refused rather than widened: a button that quietly does
					// far more than it says is worse than one that does not work.
					writeJSONError(w, http.StatusBadRequest, "no requested check is established by a probe")
					return
				}
			}
		}
		rt.checkRecordingReadinessScoped(r.Context(), scope)
	case r.URL.Path == "/health/repair" && r.Method == http.MethodPost:
		// Starts work and reports the checklist as it stands. The run outlives
		// the request — a backfill crosses the whole archive — so the row says
		// it is running and the next read tells you how it went.
		var body struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "unreadable repair request")
			return
		}
		switch body.Action {
		case repairBackfillSearch:
			if !rt.canBackfillSearch() {
				writeJSONError(w, http.StatusBadRequest, "Search re-indexing is available only for Nextcloud recording archives.")
				return
			}
			rt.startSearchBackfill()
		default:
			writeJSONError(w, http.StatusBadRequest, "unsupported repair action")
			return
		}
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "unsupported health operation")
		return
	}
	writeJSON(w, http.StatusOK, rt.readiness(r.Context()))
}

func (rt *Runtime) recordingSetupHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPut {
		writeJSONError(w, 405, "method not allowed")
		return
	}
	var body struct {
		InternalSecret *string `json:"internal_secret"`
		TestRoomURL    *string `json:"test_room_url"`
		Action         string  `json:"action"`
		JobID          string  `json:"job_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		writeJSONError(w, 400, "invalid recording setup")
		return
	}
	if body.InternalSecret != nil && strings.TrimSpace(os.Getenv(envTalkSignalingInternalSecret)) != "" {
		writeJSONError(w, 409, "The internal secret is managed by deployment configuration.")
		return
	}
	if body.TestRoomURL != nil && !rt.validTestRoom(strings.TrimSpace(*body.TestRoomURL)) {
		writeJSONError(w, 400, "Use a Talk room URL without query parameters. Configure NEXTCLOUD_URL or the Talk backend URL on the server first.")
		return
	}
	if body.Action != "" && body.Action != "arm_test" && body.Action != "confirm_playback" {
		writeJSONError(w, 400, "unknown setup action")
		return
	}
	s := &rt.recordingSetup
	// Serialize edits with probes so a changed credential cannot inherit an old pass.
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	rt.loadRecordingSetupLocked()
	if s.loadFailed {
		s.mu.Unlock()
		writeJSONError(w, 409, "Restore the saved recording setup before making changes.")
		return
	}
	next := s.state
	if body.InternalSecret != nil {
		next.InternalSecret = strings.TrimSpace(*body.InternalSecret)
	}
	if body.TestRoomURL != nil && next.TestRoomURL != strings.TrimSpace(*body.TestRoomURL) {
		next.TestRoomURL = strings.TrimSpace(*body.TestRoomURL)
		next.TestStartedAt = ""
		next.PlaybackJobID = ""
		next.PlaybackVerifiedAt = ""
	}
	if body.Action == "arm_test" {
		if !rt.validTestRoom(next.TestRoomURL) {
			// Make one rather than demanding one. The room is the only thing
			// this test ever needed configured, and Cassini can create it.
			created, err := rt.ensureTestRoom(r.Context(), next.TestRoomURL)
			if err != nil {
				s.mu.Unlock()
				rt.logger.Printf("ERROR: could not prepare a test room: %v", err)
				writeJSONError(w, 502, "Cassini could not create a test room in Talk. Check that Talk is installed and reachable, then try again.")
				return
			}
			next.TestRoomURL = created
		}
		next.TestStartedAt = nowUTCString()
		next.PlaybackJobID = ""
		next.PlaybackVerifiedAt = ""
	}
	if body.Action == "confirm_playback" {
		// checkMu keeps setup edits serialized; the database need not block
		// incoming Talk requests or credential reads on mu.
		s.mu.Unlock()
		test := rt.readinessTest(r.Context(), next)
		s.mu.Lock()
		if !test.Published || body.JobID == "" || test.JobID != body.JobID {
			s.mu.Unlock()
			writeJSONError(w, 409, "The selected Talk test recording has not finished publishing.")
			return
		}
		next.PlaybackJobID = body.JobID
		next.PlaybackVerifiedAt = nowUTCString()
	}
	if err := rt.saveRecordingSetupLocked(next); err != nil {
		s.mu.Unlock()
		writeJSONError(w, 500, "Could not save recording setup on the persistent volume.")
		return
	}
	if body.InternalSecret != nil || body.TestRoomURL != nil {
		s.checkedAt = time.Time{}
		s.checks = nil
		s.inboundAt = time.Time{}
		delete(s.probedAt, "talk")
	}
	s.mu.Unlock()
	writeJSON(w, 200, rt.readiness(r.Context()))
}

// Used by both manual and Talk-backed recording admission. Expired or missing
// network evidence is not a permanent veto; the recorder validates on connect.
func (rt *Runtime) recordingConfigurationRefusal(req TriggerRequest) string {
	if req.TalkAuthMode != talkAuthModeHPBInternal {
		return ""
	}
	if refusal := ncAccessSubstrate.recordingRefusal(); refusal != "" {
		return refusal
	}
	secret, _ := rt.signalingSecret()
	rt.recordingSetup.mu.Lock()
	unreadable := rt.recordingSetup.loadFailed
	rt.recordingSetup.mu.Unlock()
	if unreadable && secret == "" {
		return "Cassini could not read its saved recording setup. Ask an administrator to restore recording-setup.json and restart Cassini."
	}
	if secret == "" {
		return "Talk recording needs its signaling internal secret. Open Cassini → Operator → Doctor to configure it."
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		return "Talk recording credentials are unavailable. Open Cassini → Operator → Doctor."
	}
	// Diagnostic results are advisory: the administrator may have repaired
	// Nextcloud or HPB since the probe. The recorder validates the live path.

	return ""
}

// Public callers receive one coarse state. No network calls, account names,
// room URLs, secret source, job ids, or diagnostic details leave this boundary.
func (rt *Runtime) publicRecordingState(ctx context.Context) string {
	// An optional model or an incomplete search index cannot make playable
	// audio appear broken to everyone who opens the app.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return rt.readinessWithOptional(ctx, false).RecordingState
}
