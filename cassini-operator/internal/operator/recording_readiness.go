package operator

import (
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
	Steps     []readinessStep `json:"steps,omitempty"`
	CheckedAt string          `json:"checked_at,omitempty"`
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
	inboundAt  time.Time
	probe      func(context.Context, string) ([]readinessCheck, error)
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
		if c.State != "passed" && c.State != "needs_action" && c.State != "not_verified" {
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
// workdir and workdir.writable are one fact to a reader: whether Cassini can
// use its recording volume. They are reported separately because they fail for
// different reasons, which matters to doctor and not to this list, so they
// collapse into one row carrying the worse of the two.
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
	s := &rt.recordingSetup
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	rt.loadRecordingSetupLocked()
	if time.Since(s.checkedAt) < 2*time.Second {
		s.mu.Unlock()
		return
	}
	room, probe := s.state.TestRoomURL, s.probe
	s.mu.Unlock()
	if probe == nil {
		probe = rt.runConnectionProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if cfg, err := LoadExAppConfig(); err == nil && cfg.Active {
		cfg.preflightDirectShares(ctx, rt.logger)
	}
	// The panel polls GET every five seconds. Probe the media host once per
	// explicit check and retain its verdict with the time it was checked.
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
	var checks []readinessCheck
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) != "" && rt.validTestRoom(room) {
		var err error
		checks, err = probe(ctx, room)
		if err != nil {
			checks = []readinessCheck{{ID: "talk.discovery", State: "not_verified", Code: "probe_failed", Message: "The connection check did not finish. Check the recorder installation and connectivity, then retry.", Action: "recheck"}}
		}
	}
	now := time.Now().UTC()
	for i := range checks {
		checks[i].CheckedAt = now.Format(time.RFC3339)
	}
	s.mu.Lock()
	s.hostChecks = host
	// A configuration edit while the probe was running invalidates its answer.
	if s.state.TestRoomURL == room {
		s.checks = checks
		s.checkedAt = now
	}
	s.mu.Unlock()
}

// transcriptionUnavailable says why enabled transcription cannot run on device:
// the device itself, or the selected model. Empty means it can run.
func (rt *Runtime) transcriptionUnavailable(settings STTSettings, device string) string {
	if ok, detail := rt.effectiveComputeStatus(settings, device); !ok {
		return detail
	}
	if _, err := rt.admitModelForDevice(settings, device); err != nil {
		return err.Error()
	}
	return ""
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
		resp.Checks = append(resp.Checks, readinessCheck{ID: "storage", State: "passed", Code: "storage_ready", Message: "The Nextcloud storage preflight passed. A test recording verifies publication and playback.", CheckedAt: access.CheckedAt})
	} else {
		resp.Checks = append(resp.Checks, readinessCheck{ID: "storage", State: "needs_action", Code: "storage_incomplete", Message: "The Nextcloud storage preflight did not pass. Review the storage details below.", Action: "setup_storage", CheckedAt: access.CheckedAt})
	}
	if secret == "" && !failed {
		add("talk.authentication", "needs_action", "internal_secret_missing", "Enter the internal secret from your Talk signaling server.", "configure_talk")
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		add("talk.handoff", "needs_action", "recording_secret_missing", "Cassini could not provision its recording credential. Check its persistent storage.", "connect_talk")
	}
	if !rt.validTestRoom(state.TestRoomURL) {
		add("talk.discovery", "not_verified", "test_room_required", "Choose a dedicated Talk room to verify the connection without recording it.", "test_room")
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
	// Host findings lead so a full disk can explain a storage failure. GET
	// never launches the media doctor subprocess.
	if len(host) == 0 {
		host = []readinessCheck{{ID: "host", State: "not_verified", Code: "host_not_checked", Message: "The recording host has not been checked yet.", Action: "recheck"}}
	}
	resp.Checks = append(host, resp.Checks...)
	resp.Test = rt.readinessTest(ctx, state)
	resp.State = worstReadinessState(resp.Checks)
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
		core = append(core, check)
	}
	return worstReadinessState(core)
}

// worstReadinessState is the instance's worst news, ordered by how much it
// costs to ignore.
//
// `warn` sits between passed and needs_action: something is impaired but the
// thing still works, so it must neither be swallowed into "passed" nor promoted
// into a blocking failure. `not_verified` ranks below warn — nothing has been
// established, which is not the same as having found a problem.
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
		rt.checkRecordingReadiness(r.Context())
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
			s.mu.Unlock()
			writeJSONError(w, 400, "Choose a test room first.")
			return
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
		return "Talk recording needs its signaling internal secret. Open Cassini → Operator → Publish pipeline to configure it."
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		return "Talk recording credentials are unavailable. Open Cassini → Operator → Publish pipeline."
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
