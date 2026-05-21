package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultGuestName    = "CassiniRecorder"
	defaultRoomEmptySec = 30.0
	recordStopGrace     = 10 * time.Second
)

type recordProcessState struct {
	process        *os.Process
	done           chan struct{}
	stopInProgress bool
}

type recordResult struct {
	ArtifactRunPath string
	StopReason      string
	StopDetail      string
	ExitCode        *int
}

type recordLiveSignalWriter struct {
	target io.Writer
	marker []byte
	mu     sync.Mutex
	tail   []byte
	once   sync.Once
	liveCh chan struct{}
}

type triggerRequestInput struct {
	Platform          string   `json:"platform"`
	BaseURL           string   `json:"baseURL,omitempty"`
	RoomToken         string   `json:"roomToken,omitempty"`
	URL               string   `json:"url,omitempty"`
	TalkAuthMode      string   `json:"talkAuthMode,omitempty"`
	TalkConnectURL    string   `json:"talkConnectURL,omitempty"`
	GuestName         *string  `json:"guestName,omitempty"`
	DurationSeconds   *int     `json:"duration,omitempty"`
	StopWhenRoomEmpty *bool    `json:"stopWhenRoomEmpty,omitempty"`
	RoomEmptyGraceSec *float64 `json:"roomEmptyGrace,omitempty"`
}

func (rt *Runtime) executeRecordCLI(_ context.Context, job Job, req TriggerRequest) (recordResult, error) {
	req.TalkAuthMode = normalizeTalkAuthMode(req.TalkAuthMode)
	if req.TalkAuthMode == "" {
		req.TalkAuthMode = defaultTalkAuthMode
	}
	if err := rt.runRecordDoctor(); err != nil {
		return recordResult{}, err
	}

	runPath := attemptRunPath(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber)
	if err := os.MkdirAll(filepath.Dir(runPath), 0o755); err != nil {
		return recordResult{}, fmt.Errorf("create run parent dir: %w", err)
	}
	logPath, logFile, err := openAttemptLogFile(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber, "record")
	if err != nil {
		return recordResult{}, err
	}
	defer logFile.Close()
	if err := rt.store.SetAttemptStageLogPath(context.Background(), job.ID, job.CurrentAttemptNumber, "record", logPath); err != nil {
		return recordResult{}, err
	}

	args := []string{
		"record",
		"--out", runPath,
		"--name", req.GuestName,
		"--talk-auth-mode", req.TalkAuthMode,
	}
	if callURL := req.effectiveCallURL(); callURL != "" {
		args = append(args, "--call", callURL)
	}
	if baseURL := strings.TrimSpace(req.BaseURL); baseURL != "" {
		args = append(args, "--talk-base-url", strings.TrimRight(baseURL, "/"))
	}
	if roomToken := strings.TrimSpace(req.RoomToken); roomToken != "" {
		args = append(args, "--talk-room-token", roomToken)
	}
	if connectURL := rt.recordConnectURL(req); connectURL != "" {
		args = append(args, "--connect-url", connectURL)
	}
	if req.DurationSeconds != nil {
		args = append(args, "--duration", strconv.Itoa(*req.DurationSeconds))
	}
	if req.StopWhenRoomEmptySet {
		args = append(args, fmt.Sprintf("--stop-when-room-empty=%t", req.StopWhenRoomEmpty))
	}
	if req.RoomEmptyGraceSet {
		args = append(args, "--room-empty-grace", formatSeconds(req.RoomEmptyGraceSeconds))
	}

	var logCapture bytes.Buffer
	liveCh := make(chan struct{}, 1)
	stdoutWriter := newRecordLiveSignalWriter(
		io.MultiWriter(writerOrDiscard(rt.stdout), logFile, &logCapture),
		[]byte("talk recorder running:"),
		liveCh,
	)
	stderrWriter := newRecordLiveSignalWriter(
		io.MultiWriter(writerOrDiscard(rt.stderr), logFile, &logCapture),
		[]byte("talk recorder running:"),
		liveCh,
	)
	cmd := exec.Command(rt.cfg.CassiniBin, args...)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return recordResult{}, fmt.Errorf("cassini record start: %w", err)
	}

	state := rt.registerRecordProcess(job.ID, cmd.Process)
	defer rt.completeRecordProcess(job.ID)
	go rt.stopRecordProcessOnShutdown(job.ID, state)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var runErr error
	select {
	case <-liveCh:
		if err := rt.notifyTalkStartedForJob(job.ID); err != nil {
			if signalErr := state.process.Signal(syscall.SIGTERM); signalErr != nil && !isExitedProcessError(signalErr) {
				rt.logger.Printf("record stop after talk started callback failure failed id=%s: %v", job.ID, signalErr)
			}
			runErr = <-waitCh
			return recordResult{}, fmt.Errorf("talk started callback: %w", err)
		}
		runErr = <-waitCh
	case runErr = <-waitCh:
	}

	result := recordResult{
		ArtifactRunPath: runPath,
		ExitCode:        exitCodeFromRunError(runErr),
		StopDetail:      recordStopDetail(logCapture.String()),
	}
	result.StopReason = classifyRecordStopReason(state.stopInProgress, result.ExitCode, result.StopDetail, runErr)
	if runErr != nil {
		if result.StopDetail == "" {
			result.StopDetail = strings.TrimSpace(runErr.Error())
		}
		return result, fmt.Errorf("cassini record: %w", runErr)
	}
	if _, err := os.Stat(filepath.Join(runPath, "cassini.json")); err != nil {
		return result, fmt.Errorf("record output missing cassini.json: %w", err)
	}
	return result, nil
}

func (rt *Runtime) runRecordDoctor() error {
	cmd := exec.Command(rt.cfg.CassiniBin, "doctor", "--target", "record")
	cmd.Stdout = writerOrDiscard(rt.stdout)
	cmd.Stderr = writerOrDiscard(rt.stderr)
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cassini doctor --target record: %w", err)
	}
	return nil
}

func formatSeconds(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func normalizeTalkAuthMode(value string) string {
	return strings.TrimSpace(value)
}

func isValidTalkAuthMode(value string) bool {
	switch normalizeTalkAuthMode(value) {
	case talkAuthModeGuestParticipant, talkAuthModeHPBInternal:
		return true
	default:
		return false
	}
}

func (rt *Runtime) registerRecordProcess(jobID string, process *os.Process) *recordProcessState {
	state := &recordProcessState{
		process: process,
		done:    make(chan struct{}),
	}
	rt.recordMu.Lock()
	if rt.recordJobs == nil {
		rt.recordJobs = map[string]*recordProcessState{}
	}
	rt.recordJobs[jobID] = state
	rt.recordMu.Unlock()
	return state
}

func (rt *Runtime) completeRecordProcess(jobID string) {
	rt.recordMu.Lock()
	state := rt.recordJobs[jobID]
	delete(rt.recordJobs, jobID)
	rt.recordMu.Unlock()
	if state != nil {
		close(state.done)
	}
}

func (rt *Runtime) beginRecordStop(jobID string) (*recordProcessState, bool) {
	rt.recordMu.Lock()
	defer rt.recordMu.Unlock()
	state := rt.recordJobs[jobID]
	if state == nil {
		return nil, false
	}
	alreadyStopping := state.stopInProgress
	if !alreadyStopping {
		state.stopInProgress = true
	}
	return state, alreadyStopping
}

func (rt *Runtime) handleStopJob(w http.ResponseWriter, r *http.Request, id string) {
	job, err := rt.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get job: %v", err))
		return
	}
	if job.Stage != "record" || job.State != "running" {
		writeJSONError(w, http.StatusConflict, "job is not stoppable")
		return
	}

	state, alreadyStopping := rt.beginRecordStop(id)
	if state == nil {
		writeJSONError(w, http.StatusConflict, "job is not stoppable")
		return
	}
	if alreadyStopping {
		writeJSON(w, http.StatusAccepted, recordStopResponse{ID: id})
		return
	}
	requestedAt := nowUTCString()
	signalSentAt := nowUTCString()
	if err := state.process.Signal(syscall.SIGTERM); err != nil && !isExitedProcessError(err) {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("stop job: %v", err))
		return
	}
	if err := rt.store.MarkRecordStopRequested(r.Context(), id, requestedAt, signalSentAt); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("update stop request: %v", err))
		return
	}
	go rt.enforceRecordStop(id, state)
	writeJSON(w, http.StatusAccepted, recordStopResponse{ID: id})
}

func parseJobPath(path string) (id string, action string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/jobs/")
	if trimmed == "" || trimmed == path {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", false
		}
		return parts[0], "", true
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		if parts[1] != "stop" && parts[1] != "rerun" {
			return "", "", false
		}
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

func (rt *Runtime) stopRecordProcessOnShutdown(jobID string, state *recordProcessState) {
	select {
	case <-rt.ctx.Done():
		if err := state.process.Signal(syscall.SIGTERM); err != nil && !isExitedProcessError(err) {
			rt.logger.Printf("record shutdown stop failed id=%s: %v", jobID, err)
		}
	case <-state.done:
	}
}

func (rt *Runtime) enforceRecordStop(jobID string, state *recordProcessState) {
	timer := time.NewTimer(recordStopGrace)
	defer timer.Stop()
	select {
	case <-state.done:
		return
	case <-timer.C:
	}
	if err := state.process.Kill(); err != nil && !isExitedProcessError(err) {
		rt.logger.Printf("record hard kill failed id=%s: %v", jobID, err)
		return
	}
	rt.logger.Printf("record hard-killed id=%s after stop grace %s", jobID, recordStopGrace)
}

func isExitedProcessError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrProcessDone) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.ESRCH
}

func exitCodeFromRunError(err error) *int {
	if err == nil {
		code := 0
		return &code
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

func recordStopDetail(logs string) string {
	for _, line := range reverseLogLines(logs) {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "talk recorder stopping:"); idx >= 0 {
			return strings.TrimSpace(line[idx+len("talk recorder stopping:"):])
		}
	}
	return ""
}

func newRecordLiveSignalWriter(target io.Writer, marker []byte, liveCh chan struct{}) io.Writer {
	return &recordLiveSignalWriter{
		target: target,
		marker: marker,
		liveCh: liveCh,
	}
}

func (w *recordLiveSignalWriter) Write(p []byte) (int, error) {
	n, err := w.target.Write(p)
	w.signalIfMarkerSeen(p)
	return n, err
}

func (w *recordLiveSignalWriter) signalIfMarkerSeen(p []byte) {
	if len(w.marker) == 0 {
		return
	}

	w.mu.Lock()
	combined := append(append([]byte{}, w.tail...), p...)
	if maxTail := len(w.marker) - 1; maxTail > 0 {
		if len(combined) > maxTail {
			w.tail = append(w.tail[:0], combined[len(combined)-maxTail:]...)
		} else {
			w.tail = append(w.tail[:0], combined...)
		}
	}
	w.mu.Unlock()

	if bytes.Contains(combined, w.marker) {
		w.once.Do(func() {
			select {
			case w.liveCh <- struct{}{}:
			default:
			}
		})
	}
}

func reverseLogLines(logs string) []string {
	lines := strings.Split(logs, "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

func classifyRecordStopReason(stopAccepted bool, exitCode *int, stopDetail string, runErr error) string {
	if stopAccepted && runErr == nil {
		return "operator_requested"
	}
	combined := strings.ToLower(strings.TrimSpace(stopDetail))
	if combined == "" && runErr != nil {
		combined = strings.ToLower(strings.TrimSpace(runErr.Error()))
	} else if runErr != nil {
		combined = combined + " " + strings.ToLower(strings.TrimSpace(runErr.Error()))
	}
	switch {
	case strings.Contains(combined, "room empty"):
		return "room_empty"
	case strings.Contains(combined, "duration limit reached"):
		return "duration_limit"
	case strings.Contains(combined, "signaling connection error"):
		return "signaling_connection_error"
	case strings.Contains(combined, "signaling room join failed"),
		strings.Contains(combined, "all signaling hello attempts failed"),
		strings.Contains(combined, "signaling hello failed"),
		strings.Contains(combined, "signaling settings failed"),
		strings.Contains(combined, "recording-auth signaling settings failed"),
		strings.Contains(combined, "missing signaling server"),
		strings.Contains(combined, "standalone signaling required"),
		strings.Contains(combined, "hello response missing signaling sessionid"),
		strings.Contains(combined, "internal clients are not supported by the signaling server"),
		strings.Contains(combined, "internal signaling auth failed"),
		strings.Contains(combined, "signaling server did not advertise mcu/hpb support"),
		strings.Contains(combined, "join call failed"):
		return "join_failed"
	case exitCode != nil && *exitCode != 0:
		return "record_process_exit_nonzero"
	default:
		return ""
	}
}

func parseTriggerRequest(input triggerRequestInput) (TriggerRequest, error) {
	req := TriggerRequest{
		Platform:              strings.TrimSpace(input.Platform),
		BaseURL:               strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"),
		RoomToken:             strings.TrimSpace(input.RoomToken),
		URL:                   strings.TrimSpace(input.URL),
		TalkAuthMode:          normalizeTalkAuthMode(input.TalkAuthMode),
		TalkConnectURL:        strings.TrimRight(strings.TrimSpace(input.TalkConnectURL), "/"),
		GuestName:             defaultGuestName,
		StopWhenRoomEmpty:     true,
		RoomEmptyGraceSeconds: defaultRoomEmptySec,
	}
	if req.Platform == "" {
		return TriggerRequest{}, errors.New("platform is required")
	}
	if req.URL == "" && (req.BaseURL == "" || req.RoomToken == "") {
		return TriggerRequest{}, errors.New("url or baseURL + roomToken is required")
	}
	if (req.BaseURL == "") != (req.RoomToken == "") {
		return TriggerRequest{}, errors.New("baseURL and roomToken must be provided together")
	}
	if req.TalkAuthMode == "" {
		req.TalkAuthMode = defaultTalkAuthMode
	}
	if !isValidTalkAuthMode(req.TalkAuthMode) {
		return TriggerRequest{}, fmt.Errorf("talkAuthMode must be %q or %q", talkAuthModeGuestParticipant, talkAuthModeHPBInternal)
	}
	if input.GuestName != nil {
		if value := strings.TrimSpace(*input.GuestName); value != "" {
			req.GuestName = value
		}
	}
	if input.DurationSeconds != nil {
		if *input.DurationSeconds < 0 {
			return TriggerRequest{}, errors.New("duration must be >= 0")
		}
		req.DurationSeconds = input.DurationSeconds
	}
	if input.StopWhenRoomEmpty != nil {
		req.StopWhenRoomEmpty = *input.StopWhenRoomEmpty
		req.StopWhenRoomEmptySet = true
	}
	if input.RoomEmptyGraceSec != nil {
		if *input.RoomEmptyGraceSec < 0 {
			return TriggerRequest{}, errors.New("roomEmptyGrace must be >= 0")
		}
		req.RoomEmptyGraceSeconds = *input.RoomEmptyGraceSec
		req.RoomEmptyGraceSet = true
	}
	return req, nil
}

func decodeStoredTriggerRequest(raw string) (TriggerRequest, error) {
	var req TriggerRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return TriggerRequest{}, fmt.Errorf("decode stored request JSON: %w", err)
	}
	req.Platform = strings.TrimSpace(req.Platform)
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	req.TalkConnectURL = strings.TrimRight(strings.TrimSpace(req.TalkConnectURL), "/")
	req.RoomToken = strings.TrimSpace(req.RoomToken)
	req.URL = strings.TrimSpace(req.URL)
	req.TalkAuthMode = normalizeTalkAuthMode(req.TalkAuthMode)
	req.GuestName = strings.TrimSpace(req.GuestName)
	if req.TalkAuthMode == "" {
		req.TalkAuthMode = defaultTalkAuthMode
	}
	if req.Platform == "" || req.GuestName == "" || req.effectiveCallURL() == "" || !isValidTalkAuthMode(req.TalkAuthMode) {
		return TriggerRequest{}, errors.New("stored request is missing required fields")
	}
	req.StopWhenRoomEmptySet = !req.StopWhenRoomEmpty
	req.RoomEmptyGraceSet = req.RoomEmptyGraceSeconds != defaultRoomEmptySec
	return req, nil
}

func encodeTriggerRequest(req TriggerRequest) (string, error) {
	req.TalkAuthMode = normalizeTalkAuthMode(req.TalkAuthMode)
	if req.TalkAuthMode == "" {
		req.TalkAuthMode = defaultTalkAuthMode
	}
	body, err := marshalCompactJSON(req)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func marshalCompactJSON(payload any) ([]byte, error) {
	body, err := jsonMarshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request JSON: %w", err)
	}
	return body, nil
}

var jsonMarshal = func(v any) ([]byte, error) {
	return json.Marshal(v)
}

var errRecordBusy = errors.New("max record workers exceeded")

func recordAcceptError(err error) (status int, message string) {
	if errors.Is(err, errRecordBusy) {
		return http.StatusServiceUnavailable, errRecordBusy.Error()
	}
	return http.StatusInternalServerError, err.Error()
}

func (req TriggerRequest) effectiveCallURL() string {
	if strings.TrimSpace(req.URL) != "" {
		return strings.TrimSpace(req.URL)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	roomToken := strings.TrimSpace(req.RoomToken)
	if baseURL == "" || roomToken == "" {
		return ""
	}
	return baseURL + "/call/" + roomToken
}

func (rt *Runtime) recordConnectURL(req TriggerRequest) string {
	connectURL := strings.TrimRight(strings.TrimSpace(req.TalkConnectURL), "/")
	if connectURL == "" && req.Platform == nextcloudTalkProvider {
		connectURL = strings.TrimRight(strings.TrimSpace(rt.cfg.TalkBackendURL), "/")
	}
	return connectURL
}

func (req TriggerRequest) logTarget() string {
	if strings.TrimSpace(req.RoomToken) != "" {
		return strings.TrimRight(strings.TrimSpace(req.BaseURL), "/") + " room=" + strings.TrimSpace(req.RoomToken)
	}
	return strings.TrimSpace(req.URL)
}

type recordStopResponse struct {
	ID string `json:"id"`
}

type rerunJobResponse struct {
	ID            string `json:"id"`
	AttemptNumber int    `json:"attempt_number"`
}

func (rt *Runtime) handleRerunJob(w http.ResponseWriter, r *http.Request, id string) {
	job, err := rt.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get job: %v", err))
		return
	}
	if job.Stage != "done" || (job.State != "failed" && job.State != "succeeded") {
		writeJSONError(w, http.StatusConflict, "job is not eligible for rerun")
		return
	}

	queuedAt := nowUTCString()
	rerunJob, err := rt.store.QueueRerunAttempt(r.Context(), job, queuedAt)
	if err != nil {
		if errors.Is(err, ErrJobNotEligibleForRerun) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("queue rerun attempt: %v", err))
		return
	}
	if rerunJob.ArtifactRunPath == nil || strings.TrimSpace(*rerunJob.ArtifactRunPath) == "" {
		writeJSONError(w, http.StatusConflict, ErrJobNotEligibleForRerun.Error())
		return
	}

	task := buildTask{JobID: rerunJob.ID, AttemptNumber: rerunJob.CurrentAttemptNumber, ArtifactRunPath: *rerunJob.ArtifactRunPath}
	select {
	case rt.buildQueue <- task:
		rt.logger.Printf("rerun accepted id=%s attempt=%d run=%s", rerunJob.ID, rerunJob.CurrentAttemptNumber, task.ArtifactRunPath)
		writeJSON(w, http.StatusAccepted, rerunJobResponse{ID: rerunJob.ID, AttemptNumber: rerunJob.CurrentAttemptNumber})
	case <-rt.ctx.Done():
		if updateErr := rt.store.MarkBuildFailed(context.Background(), rerunJob.ID, "", "build queue stopped", nowUTCString()); updateErr != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("queue rerun attempt: %v", updateErr))
			return
		}
		writeJSONError(w, http.StatusServiceUnavailable, "build queue stopped")
	}
}
