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

type triggerRequestInput struct {
	Platform          string   `json:"platform"`
	URL               string   `json:"url"`
	GuestName         *string  `json:"guestName,omitempty"`
	DurationSeconds   *int     `json:"duration,omitempty"`
	StopWhenRoomEmpty *bool    `json:"stopWhenRoomEmpty,omitempty"`
	RoomEmptyGraceSec *float64 `json:"roomEmptyGrace,omitempty"`
}

func (rt *Runtime) executeRecordCLI(_ context.Context, job Job, req TriggerRequest) (recordResult, error) {
	if err := rt.runRecordDoctor(); err != nil {
		return recordResult{}, err
	}

	runPath := filepath.Join(rt.cfg.WorkRoot, job.ID+".run")
	if err := os.MkdirAll(filepath.Dir(runPath), 0o755); err != nil {
		return recordResult{}, fmt.Errorf("create run parent dir: %w", err)
	}

	args := []string{
		"record",
		"--call", req.URL,
		"--out", runPath,
		"--name", req.GuestName,
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
	cmd := exec.Command(rt.cfg.CassiniBin, args...)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), &logCapture)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), &logCapture)
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return recordResult{}, fmt.Errorf("cassini record start: %w", err)
	}

	state := rt.registerRecordProcess(job.ID, cmd.Process)
	defer rt.completeRecordProcess(job.ID)
	go rt.stopRecordProcessOnShutdown(job.ID, state)

	runErr := cmd.Wait()
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
		if parts[1] != "stop" {
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
		strings.Contains(combined, "signaling settings failed"),
		strings.Contains(combined, "missing signaling server"),
		strings.Contains(combined, "hello response missing signaling sessionid"),
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
		URL:                   strings.TrimSpace(input.URL),
		GuestName:             defaultGuestName,
		StopWhenRoomEmpty:     true,
		RoomEmptyGraceSeconds: defaultRoomEmptySec,
	}
	if req.Platform == "" {
		return TriggerRequest{}, errors.New("platform is required")
	}
	if req.URL == "" {
		return TriggerRequest{}, errors.New("url is required")
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

func encodeTriggerRequest(req TriggerRequest) (string, error) {
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

type recordStopResponse struct {
	ID string `json:"id"`
}
