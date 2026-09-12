package operator

import (
	"context"
	"encoding/json"
	"errors"
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
	ID        string `json:"id"`
	State     string `json:"state"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Action    string `json:"action,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
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
	State            string           `json:"state"`
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

// Coalesce concurrent checks and put a ceiling on network/process work. GET
// never launches a process. Every result expires, including successful ones.
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
		cfg.preflightNCStorage(ctx, rt.logger)
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
	// A configuration edit while the probe was running invalidates its answer.
	if s.state.TestRoomURL == room {
		s.checks = checks
		s.checkedAt = now
	}
	s.mu.Unlock()
}

func (rt *Runtime) readiness(ctx context.Context) readinessResponse {
	secret, source := rt.signalingSecret()
	s := &rt.recordingSetup
	s.mu.Lock()
	rt.loadRecordingSetupLocked()
	state, failed, checkedAt, inbound := s.state, s.loadFailed, s.checkedAt, s.inboundAt
	probes := append([]readinessCheck(nil), s.checks...)
	s.mu.Unlock()
	resp := readinessResponse{State: "passed", Checks: []readinessCheck{}, SecretConfigured: secret != "", SecretSource: source, TestRoomURL: state.TestRoomURL}
	add := func(id, state, code, message, action string) {
		resp.Checks = append(resp.Checks, readinessCheck{ID: id, State: state, Code: code, Message: message, Action: action})
	}
	if failed {
		add("configuration", "needs_action", "setup_store_unreadable", "Cassini could not read its saved recording setup. Check the persistent volume and restore recording-setup.json.", "repair_configuration")
	}
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	storageChecked, storageTimeErr := time.Parse(time.RFC3339, access.CheckedAt)
	if !access.Applicable {
		add("storage", "not_verified", "storage_not_probed", "The Nextcloud storage check does not apply to this publish destination. Verify storage with a test recording.", "test_recording")
	} else if ncAccessSubstrate.recordingRefusal() != "" {
		add("storage", "needs_action", "storage_admission_blocked", "Cassini currently blocks recording on its stored storage status. Review the storage details below and check again after repairing them.", "setup_storage")
	} else if storageTimeErr != nil || time.Since(storageChecked) > readinessTTL {
		add("storage", "not_verified", "storage_check_expired", "There is no recent Nextcloud storage check. Check again to refresh it.", "recheck")
	} else if access.OK {
		resp.Checks = append(resp.Checks, readinessCheck{ID: "storage", State: "passed", Code: "storage_ready", Message: "The Nextcloud storage preflight passed. A test recording verifies publication and playback.", CheckedAt: access.CheckedAt})
	} else {
		resp.Checks = append(resp.Checks, readinessCheck{ID: "storage", State: "needs_action", Code: "storage_incomplete", Message: "The Nextcloud storage preflight did not pass. Review the storage details below.", Action: "setup_storage", CheckedAt: access.CheckedAt})
	}
	settings := rt.currentSettings()
	device := rt.effectiveFor(settings).Device
	if ok, detail := rt.effectiveComputeStatus(settings, device); ok {
		add("processing", "passed", "processing_ready", "Speech-processing prerequisites passed for "+device+".", "")
	} else {
		add("processing", "needs_action", "processing_unavailable", detail, "settings")
	}
	if secret == "" && !failed {
		add("talk.authentication", "needs_action", "internal_secret_missing", "Enter the internal secret from your Talk signaling server.", "configure_talk")
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		add("talk.handoff", "needs_action", "recording_secret_missing", "Cassini could not provision its recording credential. Check its persistent storage.", "connect_talk")
	}
	if !rt.validTestRoom(state.TestRoomURL) {
		add("talk.discovery", "not_verified", "test_room_required", "Choose a dedicated Talk room to verify the connection without recording it.", "test_room")
	} else if time.Since(checkedAt) > readinessTTL || len(probes) == 0 {
		add("talk.discovery", "not_verified", "connection_not_verified", "Check the Talk connection. Previous results have expired or this process restarted.", "recheck")
	} else {
		resp.Checks = append(resp.Checks, probes...)
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		// The actionable handoff row above already describes the missing credential.
	} else if !inbound.IsZero() && time.Since(inbound) < readinessTTL {
		resp.Checks = append(resp.Checks, readinessCheck{ID: "talk.handoff", State: "passed", Code: "talk_request_received", Message: "Talk recently sent an authenticated recording request to Cassini.", CheckedAt: inbound.UTC().Format(time.RFC3339)})
	} else {
		add("talk.handoff", "not_verified", "handoff_not_verified", "No recent recording request from Talk. Check again verifies outbound connectivity; a new Talk recording verifies this incoming connection. Any previous playback confirmation is shown below.", "test_recording")
	}
	resp.Test = rt.readinessTest(ctx, state)
	if resp.Test.PlaybackVerifiedAt == "" {
		add("test", "not_verified", "test_not_verified", "Record a short test through Talk, then open it and confirm playback.", "test_recording")
	}
	for _, c := range resp.Checks {
		if c.State == "needs_action" {
			resp.State = "needs_action"
			break
		}
		if c.State != "passed" {
			resp.State = "not_verified"
		}
	}
	return resp
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
	case r.URL.Path == "/readiness" && r.Method == http.MethodGet:
	case r.URL.Path == "/readiness/check" && r.Method == http.MethodPost:
		rt.checkRecordingReadiness(r.Context())
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "unsupported readiness operation")
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
		return "Talk recording needs its signaling internal secret. Open Cassini → Setup to configure it."
	}
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		return "Talk recording credentials are unavailable. Open Cassini → Setup."
	}
	// Diagnostic results are advisory: the administrator may have repaired
	// Nextcloud or HPB since the probe. The recorder validates the live path.

	return ""
}

// Public callers receive one coarse state. No network calls, account names,
// room URLs, secret source, job ids, or diagnostic details leave this boundary.
func (rt *Runtime) publicRecordingState(ctx context.Context) string {
	// Use the same aggregate as the admin report, including storage, compute,
	// and the current published test job. Return only the coarse state.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return rt.readiness(ctx).State
}
