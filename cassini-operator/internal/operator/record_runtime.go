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
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultGuestName    = "CassiniRecorder"
	defaultRoomEmptySec = 30.0
	// recordRunningMarker and recordStoppingMarker are printed by `cassini
	// record`; the operator watches the child's output for them.
	recordRunningMarker  = "talk recorder running:"
	recordStoppingMarker = "talk recorder stopping:"
	// recordStopAckGrace is how long a SIGTERMed recorder may stay silent
	// before acknowledging the stop (recordStoppingMarker) without being
	// presumed wedged; output activity extends it.
	recordStopAckGrace = 10 * time.Second
	// recordStopFinalizeGrace caps the stop as a whole. After acknowledging
	// SIGTERM the recorder composes the final MKV (depacketize + ffmpeg per
	// stream + merge), which scales with recording length and routinely
	// exceeds any flat few-second grace; SIGKILLing mid-compose destroys the
	// recording (D-350), so the ceiling is generous.
	recordStopFinalizeGrace = 30 * time.Minute
	// recordShutdownWait bounds how long operator shutdown waits for
	// in-flight record jobs; per-process stop enforcement guarantees the
	// recorder is gone within the finalize grace, so this only adds slack
	// for post-record bookkeeping (promotion, callbacks, upload).
	recordShutdownWait = recordStopFinalizeGrace + time.Minute
)

type recordProcessState struct {
	process *os.Process
	done    chan struct{}
	// stopInProgress is set by stop requests under recordMu but read by the
	// record goroutine without it, so it must be atomic (D-364).
	stopInProgress atomic.Bool
	// stopping is closed when the recorder prints recordStoppingMarker,
	// i.e. it acknowledged SIGTERM and started finalizing.
	stopping chan struct{}
	output   *recordOutputActivity
}

type recordResult struct {
	ArtifactRunPath string
	StopReason      string
	StopDetail      string
	ExitCode        *int
}

type recordLiveSignalWriter struct {
	target   io.Writer
	activity *recordOutputActivity
	mu       sync.Mutex
	watches  []*recordMarkerWatch
}

// recordMarkerWatch scans one output stream for a marker, buffering a tail so
// markers split across writes are still found, and fires signal on a match.
type recordMarkerWatch struct {
	marker []byte
	signal func()
	tail   []byte
}

// syncBuffer is a mutex-guarded bytes.Buffer. The record path hands one
// shared capture sink to both the stdout and stderr pipelines, which os/exec
// pumps from two goroutines — a bare bytes.Buffer there is a data race that
// corrupts stop-detail classification at best and panics at worst (D-364).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// recordOutputActivity tracks when the recorder last wrote to stdout/stderr;
// stop enforcement uses it to tell a busy recorder from a wedged one.
type recordOutputActivity struct {
	lastWriteNS atomic.Int64
}

func newRecordOutputActivity() *recordOutputActivity {
	activity := &recordOutputActivity{}
	activity.touch()
	return activity
}

func (a *recordOutputActivity) touch() {
	a.lastWriteNS.Store(time.Now().UnixNano())
}

func (a *recordOutputActivity) idleFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - a.lastWriteNS.Load())
}

type triggerRequestInput struct {
	Platform          string   `json:"platform"`
	URL               string   `json:"url"`
	TalkConnectURL    string   `json:"talkConnectURL,omitempty"`
	GuestName         *string  `json:"guestName,omitempty"`
	DurationSeconds   *int     `json:"duration,omitempty"`
	StopWhenRoomEmpty *bool    `json:"stopWhenRoomEmpty,omitempty"`
	RoomEmptyGraceSec *float64 `json:"roomEmptyGrace,omitempty"`
}

func (rt *Runtime) executeRecordCLI(_ context.Context, job Job, req TriggerRequest) (recordResult, error) {
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
		"--call", req.effectiveCallURL(),
		"--out", runPath,
		"--name", req.GuestName,
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

	// logCapture is shared by the stdout and stderr pipelines; os/exec pumps
	// distinct writers from separate goroutines, so the sink must serialize
	// its writes (D-364).
	logCapture := &syncBuffer{}
	liveCh := make(chan struct{})
	stoppingCh := make(chan struct{})
	signalLive := closeOnce(liveCh)
	signalStopping := closeOnce(stoppingCh)
	activity := newRecordOutputActivity()
	stdoutWriter := newRecordLiveSignalWriter(
		io.MultiWriter(writerOrDiscard(rt.stdout), logFile, logCapture),
		activity,
		newRecordMarkerWatch(recordRunningMarker, signalLive),
		newRecordMarkerWatch(recordStoppingMarker, signalStopping),
	)
	stderrWriter := newRecordLiveSignalWriter(
		io.MultiWriter(writerOrDiscard(rt.stderr), logFile, logCapture),
		activity,
		newRecordMarkerWatch(recordRunningMarker, signalLive),
		newRecordMarkerWatch(recordStoppingMarker, signalStopping),
	)
	cmd := exec.Command(rt.cfg.CassiniBin, args...)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	cmd.Env = os.Environ()
	// Run the recorder in its own process group so a hard kill also reaps
	// ffmpeg children spawned during compose instead of leaking them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return recordResult{}, fmt.Errorf("cassini record start: %w", err)
	}

	state := rt.registerRecordProcess(job.ID, cmd.Process, stoppingCh, activity)
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
	result.StopReason = classifyRecordStopReason(state.stopInProgress.Load(), result.ExitCode, result.StopDetail, runErr)
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

func (rt *Runtime) registerRecordProcess(jobID string, process *os.Process, stopping chan struct{}, output *recordOutputActivity) *recordProcessState {
	if output == nil {
		output = newRecordOutputActivity()
	}
	state := &recordProcessState{
		process:  process,
		done:     make(chan struct{}),
		stopping: stopping,
		output:   output,
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
	state := rt.recordJobs[jobID]
	rt.recordMu.Unlock()
	if state == nil {
		return nil, false
	}
	return state, state.stopInProgress.Swap(true)
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
			return
		}
		// Operator shutdown finalizes like a user stop: wait for the
		// recorder to compose the final MKV, hard-kill only on no progress.
		rt.enforceRecordStop(jobID, state)
	case <-state.done:
	}
}

// enforceRecordStop hard-kills a stop-requested recorder only when it stops
// making progress. On SIGTERM the recorder acknowledges with
// recordStoppingMarker and then composes the final MKV, which scales with the
// recording length — far beyond any flat grace — so SIGKILLing on a fixed
// timer destroyed long recordings mid-compose (D-350). The short ack grace
// (extended while the child keeps writing output) catches a wedged recorder;
// an acknowledged finalization only has the generous overall ceiling.
func (rt *Runtime) enforceRecordStop(jobID string, state *recordProcessState) {
	overall := time.NewTimer(rt.recordStopFinalizeGrace)
	defer overall.Stop()
	ackTimer := time.NewTimer(rt.recordStopAckGrace)
	defer ackTimer.Stop()
	ackCh := ackTimer.C
	stopping := state.stopping
	for {
		select {
		case <-state.done:
			return
		case <-stopping:
			// Stop acknowledged: the recorder is finalizing. Only the
			// overall ceiling applies from here.
			ackTimer.Stop()
			ackCh = nil
			stopping = nil
		case <-ackCh:
			if idle := state.output.idleFor(); idle < rt.recordStopAckGrace {
				ackTimer.Reset(rt.recordStopAckGrace - idle)
				continue
			}
			rt.hardKillRecordProcess(jobID, state, fmt.Sprintf("no stop acknowledgement within %s", rt.recordStopAckGrace))
			return
		case <-overall.C:
			rt.hardKillRecordProcess(jobID, state, fmt.Sprintf("stop exceeded finalize grace %s", rt.recordStopFinalizeGrace))
			return
		}
	}
}

func (rt *Runtime) hardKillRecordProcess(jobID string, state *recordProcessState, reason string) {
	if err := killProcessGroup(state.process); err != nil && !isExitedProcessError(err) {
		rt.logger.Printf("record hard kill failed id=%s: %v", jobID, err)
		return
	}
	rt.logger.Printf("record hard-killed id=%s: %s", jobID, reason)
}

// killProcessGroup SIGKILLs the child's process group (record/build/publish
// children run with Setpgid) so ffmpeg-style grandchildren die with it
// instead of leaking, falling back to the lone process when the group is
// already gone.
func killProcessGroup(process *os.Process) error {
	if process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return process.Kill()
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
		if idx := strings.Index(line, recordStoppingMarker); idx >= 0 {
			return strings.TrimSpace(line[idx+len(recordStoppingMarker):])
		}
	}
	return ""
}

func newRecordLiveSignalWriter(target io.Writer, activity *recordOutputActivity, watches ...*recordMarkerWatch) io.Writer {
	return &recordLiveSignalWriter{
		target:   target,
		activity: activity,
		watches:  watches,
	}
}

func newRecordMarkerWatch(marker string, signal func()) *recordMarkerWatch {
	return &recordMarkerWatch{
		marker: []byte(marker),
		signal: signal,
	}
}

// closeOnce returns a func that closes ch exactly once; the stdout and stderr
// watchers for one marker share it so either stream can fire the signal.
func closeOnce(ch chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() { close(ch) })
	}
}

func (w *recordLiveSignalWriter) Write(p []byte) (int, error) {
	n, err := w.target.Write(p)
	if w.activity != nil {
		w.activity.touch()
	}
	w.mu.Lock()
	for _, watch := range w.watches {
		watch.observe(p)
	}
	w.mu.Unlock()
	return n, err
}

func (watch *recordMarkerWatch) observe(p []byte) {
	if len(watch.marker) == 0 {
		return
	}

	combined := append(append([]byte{}, watch.tail...), p...)
	if maxTail := len(watch.marker) - 1; maxTail > 0 {
		if len(combined) > maxTail {
			watch.tail = append(watch.tail[:0], combined[len(combined)-maxTail:]...)
		} else {
			watch.tail = append(watch.tail[:0], combined...)
		}
	}

	if bytes.Contains(combined, watch.marker) {
		watch.signal()
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
		BaseURL:               "",
		RoomToken:             "",
		URL:                   strings.TrimSpace(input.URL),
		TalkConnectURL:        strings.TrimRight(strings.TrimSpace(input.TalkConnectURL), "/"),
		GuestName:             defaultGuestName,
		StopWhenRoomEmpty:     true,
		RoomEmptyGraceSeconds: defaultRoomEmptySec,
	}
	if req.Platform == "" {
		return TriggerRequest{}, errors.New("platform is required")
	}
	if req.URL == "" && (strings.TrimSpace(req.BaseURL) == "" || strings.TrimSpace(req.RoomToken) == "") {
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

func decodeStoredTriggerRequest(raw string) (TriggerRequest, error) {
	var req TriggerRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return TriggerRequest{}, fmt.Errorf("decode stored request JSON: %w", err)
	}
	req.Platform = strings.TrimSpace(req.Platform)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.TalkConnectURL = strings.TrimRight(strings.TrimSpace(req.TalkConnectURL), "/")
	req.RoomToken = strings.TrimSpace(req.RoomToken)
	req.URL = strings.TrimSpace(req.URL)
	req.GuestName = strings.TrimSpace(req.GuestName)
	if req.Platform == "" || req.GuestName == "" || req.effectiveCallURL() == "" {
		return TriggerRequest{}, errors.New("stored request is missing required fields")
	}
	req.StopWhenRoomEmptySet = !req.StopWhenRoomEmpty
	req.RoomEmptyGraceSet = req.RoomEmptyGraceSeconds != defaultRoomEmptySec
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
