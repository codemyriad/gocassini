package operator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// talkRandomBytes is the byte length of the per-request random value sent
// in Talk-Recording-Random; Talk rejects shorter values, so this is the
// minimum the protocol guarantees to accept.
const talkRandomBytes = 32

// talkRandom returns a 64-hex-character random string for the
// Talk-Recording-Random header. Spreed requires at least 32 characters
// (and a fresh value per request) — using crypto/rand keeps the values
// unique and unforgeable.
func talkRandom() string {
	buf := make([]byte, talkRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("read random: %w", err))
	}
	return hex.EncodeToString(buf)
}

const (
	talkRecordingBackendHeader     = "Talk-Recording-Backend"
	talkRecordingRandomHeader      = "Talk-Recording-Random"
	talkRecordingChecksumHeader    = "Talk-Recording-Checksum"
	talkRecordingRandomOCSHeader   = "TALK_RECORDING_RANDOM"
	talkRecordingChecksumOCSHeader = "TALK_RECORDING_CHECKSUM"
)

// Talk delivery runs while the only record slot is still held, so every
// request to Nextcloud must be bounded: one hung connection through
// http.DefaultClient used to wedge all recording capacity until restart
// (D-352).
const (
	// talkJSONRequestTimeout bounds the small JSON callbacks
	// (started/stopped/failed) end to end.
	talkJSONRequestTimeout = 30 * time.Second
	// talkUploadResponseHeaderTimeout bounds how long Nextcloud may take to
	// answer after the recording body has been fully uploaded (it moves the
	// file into the owner's storage before responding).
	talkUploadResponseHeaderTimeout = 5 * time.Minute
	// talkUploadStallGrace is how long the upload body may make no progress
	// before the stall watchdog cancels the request; an overall timeout
	// would cap legitimate long uploads of big recordings.
	talkUploadStallGrace = 2 * time.Minute
)

// talkDeliveryRetryDelays is the bounded backoff schedule for delivery
// requests; transient failures (transport errors, 5xx) are retried, 4xx
// responses are definitive.
var talkDeliveryRetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}

type talkWelcomeResponse struct {
	Version int `json:"version"`
}

type healthzResponse struct {
	OK      bool   `json:"ok"`
	Check   string `json:"check"`
	Version int    `json:"version"`
	Error   string `json:"error,omitempty"`
}

type talkRequestAuth struct {
	BackendURL string
	Random     string
	Checksum   string
}

type talkRoomRequest struct {
	Type  string         `json:"type"`
	Start *talkStartData `json:"start,omitempty"`
	Stop  *talkStopData  `json:"stop,omitempty"`
}

type talkStartData struct {
	Status int            `json:"status,omitempty"`
	Owner  string         `json:"owner"`
	Actor  *talkActorData `json:"actor"`
}

type talkStopData struct {
	Actor *talkActorData `json:"actor,omitempty"`
}

func (d *talkStopData) UnmarshalJSON(body []byte) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return nil
	}
	if body[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		body = items[0]
	}
	type stopData talkStopData
	var parsed stopData
	if err := json.Unmarshal(body, &parsed); err != nil {
		return err
	}
	*d = talkStopData(parsed)
	return nil
}

type talkActorData struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type talkRoomState struct {
	RoomKey    string
	JobID      string
	BackendURL string
	RoomToken  string
	Owner      string
	// RoomName is the Talk conversation's display name, resolved
	// asynchronously after start (see talk_room_name.go). Empty when the
	// lookup is disabled or failed; consumers must treat it as optional.
	RoomName   string
	Status     int
	StartActor *talkActorData
	StopActor  *talkActorData
}

func (rt *Runtime) talkWelcomeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, talkWelcomeResponse{Version: 1})
}

func (rt *Runtime) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	check := strings.TrimSpace(r.URL.Query().Get("check"))
	if check == "" {
		check = "shallow"
	}

	resp := healthzResponse{
		OK:      true,
		Check:   check,
		Version: 1,
	}
	status := http.StatusOK

	switch check {
	case "shallow":
	case "record":
		// The deep check execs `cassini doctor`; the endpoint is unauthenticated,
		// so the probe is singleflighted, TTL-cached, and exec-bounded (D-376).
		if err := rt.recordHealth.check(); err != nil {
			resp.OK = false
			resp.Error = err.Error()
			status = http.StatusServiceUnavailable
		}
	default:
		resp.OK = false
		resp.Error = fmt.Sprintf("unknown check %q", check)
		status = http.StatusBadRequest
	}

	writeJSON(w, status, resp)
}

func (rt *Runtime) talkRoomHandler(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/api/v1/room/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	auth, err := rt.validateTalkRequest(r, body)
	if err != nil {
		writeTalkAuthError(w, http.StatusForbidden, "The request could not be authenticated.")
		return
	}

	var payload talkRoomRequest
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}

	switch strings.TrimSpace(payload.Type) {
	case "start":
		rt.handleTalkStart(w, r, auth, token, payload)
	case "stop":
		rt.handleTalkStop(w, r, auth, token, payload)
	default:
		writeJSONError(w, http.StatusBadRequest, "unknown talk recording request type")
	}
}

func (rt *Runtime) handleTalkStart(w http.ResponseWriter, r *http.Request, auth talkRequestAuth, token string, payload talkRoomRequest) {
	if payload.Start == nil {
		writeJSONError(w, http.StatusBadRequest, "missing start payload")
		return
	}
	if strings.TrimSpace(payload.Start.Owner) == "" {
		writeJSONError(w, http.StatusBadRequest, "start.owner is required")
		return
	}
	if payload.Start.Actor == nil || strings.TrimSpace(payload.Start.Actor.Type) == "" || strings.TrimSpace(payload.Start.Actor.ID) == "" {
		writeJSONError(w, http.StatusBadRequest, "start.actor is required")
		return
	}

	publicBaseURL := strings.TrimRight(auth.BackendURL, "/")
	operatorBaseURL := rt.operatorTalkBackendURL(publicBaseURL)
	roomKey := talkRoomKey(publicBaseURL, token)
	state := &talkRoomState{
		RoomKey:    roomKey,
		BackendURL: operatorBaseURL,
		RoomToken:  token,
		Owner:      strings.TrimSpace(payload.Start.Owner),
		Status:     payload.Start.Status,
		StartActor: payload.Start.Actor,
	}
	// Claim the room key in the same critical section as the duplicate check
	// so concurrent duplicate starts cannot both pass the lookup (D-364).
	if !rt.reserveTalkRoom(state) {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	req := TriggerRequest{
		Platform:              nextcloudTalkProvider,
		BaseURL:               publicBaseURL,
		TalkConnectURL:        operatorBaseURL,
		RoomToken:             token,
		TalkAuthMode:          talkAuthModeHPBInternal,
		GuestName:             defaultGuestName,
		StopWhenRoomEmpty:     true,
		RoomEmptyGraceSeconds: defaultRoomEmptySec,
	}
	requestBody, err := encodeTriggerRequest(req)
	if err != nil {
		rt.releaseTalkRoom(roomKey)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp, startRecord, err := rt.prepareRecordJob(r.Context(), nextcloudTalkProvider, requestBody, req)
	if err != nil {
		rt.releaseTalkRoom(roomKey)
		status, msg := recordAcceptError(err)
		writeJSONError(w, status, msg)
		return
	}
	// Bind the job ID before the record goroutine exists: its deferred
	// cleanup is keyed by job ID, and a job that fails instantly used to
	// race past this bind, leaving a permanently stale "already recording"
	// room entry (D-364).
	rt.bindTalkRoomJob(state, resp.ID)
	// Persist the binding so a restart mid-recording can still clean up the
	// spreed room state and a rerun can re-deliver the recording (D-352).
	// Failure to persist degrades recovery but must not block the start.
	if bindingJSON, err := encodeTalkBinding(state); err != nil {
		rt.logger.Printf("talk binding encode failed id=%s: %v", resp.ID, err)
	} else if err := rt.store.SetJobTalkBinding(r.Context(), resp.ID, bindingJSON); err != nil {
		rt.logger.Printf("talk binding persist failed id=%s: %v", resp.ID, err)
	}
	startRecord()
	// Resolve the room's display name off the start path; the build flow
	// embeds it as the packed meeting's title (see talk_room_name.go).
	go rt.resolveTalkRoomName(resp.ID, state.Owner, state.RoomToken)
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (rt *Runtime) handleTalkStop(w http.ResponseWriter, r *http.Request, auth talkRequestAuth, token string, payload talkRoomRequest) {
	if payload.Stop == nil {
		writeJSONError(w, http.StatusBadRequest, "missing stop payload")
		return
	}
	baseURL := strings.TrimRight(auth.BackendURL, "/")
	roomKey := talkRoomKey(baseURL, token)
	state, exists := rt.lookupTalkRoomState(roomKey)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "recording not found")
		return
	}
	if payload.Stop.Actor != nil {
		rt.updateTalkStopActor(state.JobID, payload.Stop.Actor)
	}
	rt.handleStopJob(w, r, state.JobID)
}

func (rt *Runtime) validateTalkRequest(r *http.Request, body []byte) (talkRequestAuth, error) {
	if strings.TrimSpace(rt.cfg.TalkSharedSecret) == "" {
		return talkRequestAuth{}, errors.New("talk shared secret is not configured")
	}

	auth := talkRequestAuth{
		BackendURL: strings.TrimSpace(r.Header.Get(talkRecordingBackendHeader)),
		Random:     firstHeaderValue(r, talkRecordingRandomHeader, talkRecordingRandomOCSHeader),
		Checksum:   firstHeaderValue(r, talkRecordingChecksumHeader, talkRecordingChecksumOCSHeader),
	}
	if auth.BackendURL == "" {
		return talkRequestAuth{}, fmt.Errorf("missing %s header", talkRecordingBackendHeader)
	}
	if auth.Random == "" {
		return talkRequestAuth{}, fmt.Errorf("missing %s header", talkRecordingRandomHeader)
	}
	if auth.Checksum == "" {
		return talkRequestAuth{}, fmt.Errorf("missing %s header", talkRecordingChecksumHeader)
	}

	expected := talkChecksum(rt.cfg.TalkSharedSecret, auth.Random, body)
	if !hmac.Equal([]byte(strings.ToLower(auth.Checksum)), []byte(expected)) {
		return talkRequestAuth{}, errors.New("talk recording checksum verification failed")
	}

	return auth, nil
}

func firstHeaderValue(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func talkChecksum(secret, random string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(random))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func writeTalkAuthError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]string{
			"code":    "invalid_request",
			"message": message,
		},
	})
}

func marshalJSONBody(payload any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return body
}

func talkRoomKey(baseURL, roomToken string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "|" + strings.TrimSpace(roomToken)
}

func (rt *Runtime) operatorTalkBackendURL(requestBackendURL string) string {
	if override := strings.TrimRight(strings.TrimSpace(rt.cfg.TalkBackendURL), "/"); override != "" {
		return override
	}
	return strings.TrimRight(strings.TrimSpace(requestBackendURL), "/")
}

// reserveTalkRoom claims a room key for a recording about to be accepted.
// The duplicate-start check and the map insert share one critical section so
// two concurrent starts for the same room cannot both pass the lookup.
func (rt *Runtime) reserveTalkRoom(state *talkRoomState) bool {
	rt.recordMu.Lock()
	defer rt.recordMu.Unlock()
	if _, exists := rt.talkRooms[state.RoomKey]; exists {
		return false
	}
	rt.talkRooms[state.RoomKey] = state
	return true
}

// bindTalkRoomJob attaches the accepted job ID to a reserved room state. It
// must run before the record goroutine is started: the goroutine's deferred
// cleanup is keyed by job ID.
func (rt *Runtime) bindTalkRoomJob(state *talkRoomState, jobID string) {
	rt.recordMu.Lock()
	state.JobID = jobID
	rt.talkJobs[jobID] = state
	rt.recordMu.Unlock()
}

// releaseTalkRoom drops a reservation whose record job was never accepted.
func (rt *Runtime) releaseTalkRoom(roomKey string) {
	rt.recordMu.Lock()
	delete(rt.talkRooms, roomKey)
	rt.recordMu.Unlock()
}

// lookupTalkRoomState and lookupTalkJobState return snapshot copies: the
// stored state is mutated under recordMu (JobID, StopActor), so handing out
// the shared pointer would let the record goroutine read those fields
// unsynchronized (D-364). The actor pointers inside the copy are safe to
// share — actors are replaced wholesale, never mutated in place.
func (rt *Runtime) lookupTalkRoomState(roomKey string) (talkRoomState, bool) {
	rt.recordMu.Lock()
	defer rt.recordMu.Unlock()
	state, ok := rt.talkRooms[roomKey]
	if !ok {
		return talkRoomState{}, false
	}
	return *state, true
}

func (rt *Runtime) lookupTalkJobState(jobID string) (talkRoomState, bool) {
	rt.recordMu.Lock()
	defer rt.recordMu.Unlock()
	state, ok := rt.talkJobs[jobID]
	if !ok {
		return talkRoomState{}, false
	}
	return *state, true
}

func (rt *Runtime) updateTalkStopActor(jobID string, actor *talkActorData) {
	rt.recordMu.Lock()
	if state, ok := rt.talkJobs[jobID]; ok {
		state.StopActor = actor
	}
	rt.recordMu.Unlock()
}

// errTalkRoomLive marks a stale room-scoped callback that was skipped because
// another job is currently recording in the same room. It is not retryable
// (neither a talkHTTPError nor a url.Error), so withTalkRetry returns it
// immediately.
var errTalkRoomLive = errors.New("another recording is live in this room")

// talkRoomLiveWithOtherJob reports whether the room token currently has a
// live in-memory recording owned by a different job (or one still being
// reserved). Stale stopped/failed callbacks for an old job are keyed only by
// room token, so sending them while a newer recording is live would tell
// spreed the LIVE recording stopped/failed (D-352). The persisted binding
// does not retain the in-memory RoomKey — it is derived from the
// spreed-supplied public backend URL while the binding may hold the
// cfg.TalkBackendURL override — so the match is by room token; tokens are
// unique per Nextcloud instance and an operator serves a single instance.
func (rt *Runtime) talkRoomLiveWithOtherJob(roomToken, jobID string) (string, bool) {
	rt.recordMu.Lock()
	defer rt.recordMu.Unlock()
	for _, state := range rt.talkRooms {
		if state.RoomToken == roomToken && state.JobID != jobID {
			return state.JobID, true
		}
	}
	return "", false
}

func (rt *Runtime) clearTalkRoomJobByID(jobID string) {
	rt.recordMu.Lock()
	if state, ok := rt.talkJobs[jobID]; ok {
		delete(rt.talkRooms, state.RoomKey)
		delete(rt.talkJobs, jobID)
	}
	rt.recordMu.Unlock()
}

func (rt *Runtime) notifyTalkStarted(state talkRoomState) error {
	payload := map[string]any{
		"type": "started",
		"started": map[string]any{
			"token":  state.RoomToken,
			"status": state.Status,
			"actor": map[string]string{
				"type": state.StartActor.Type,
				"id":   state.StartActor.ID,
			},
		},
	}
	return rt.postTalkJSON(state.BackendURL, "/ocs/v2.php/apps/spreed/api/v1/recording/backend", payload)
}

func (rt *Runtime) notifyTalkStartedForJob(jobID string) error {
	state, ok := rt.lookupTalkJobState(jobID)
	if !ok {
		return nil
	}
	return rt.notifyTalkStarted(state)
}

func (rt *Runtime) notifyTalkStopped(state talkRoomState) error {
	stopped := map[string]any{
		"token": state.RoomToken,
	}
	if state.StopActor != nil && strings.TrimSpace(state.StopActor.Type) != "" && strings.TrimSpace(state.StopActor.ID) != "" {
		stopped["actor"] = map[string]string{
			"type": state.StopActor.Type,
			"id":   state.StopActor.ID,
		}
	}
	return rt.postTalkJSON(state.BackendURL, "/ocs/v2.php/apps/spreed/api/v1/recording/backend", map[string]any{
		"type":    "stopped",
		"stopped": stopped,
	})
}

func (rt *Runtime) notifyTalkFailed(state talkRoomState) error {
	return rt.postTalkJSON(state.BackendURL, "/ocs/v2.php/apps/spreed/api/v1/recording/backend", map[string]any{
		"type": "failed",
		"failed": map[string]string{
			"token": state.RoomToken,
		},
	})
}

func (rt *Runtime) postTalkJSON(backendURL, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal talk JSON payload: %w", err)
	}
	random := talkRandom()
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(backendURL, "/")+path, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build talk JSON request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OCS-ApiRequest", "true")
	req.Header.Set(talkRecordingRandomHeader, random)
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, random, body))
	return rt.doTalkRequest(rt.talkJSONClient, req)
}

// uploadTalkRecording streams the recording to spreed's store endpoint. The
// multipart body is piped instead of buffered — buffering the whole MKV in
// memory was an OOM on the success path of long recordings (D-352) — with
// the exact Content-Length precomputed so the request stays
// protocol-compatible (the checksum covers only the room token, matching the
// reference nextcloud-talk-recording uploader). A stall watchdog cancels the
// request when no body bytes move for talkUploadStall; once the body is
// fully written, the client's ResponseHeaderTimeout bounds the wait.
func (rt *Runtime) uploadTalkRecording(state talkRoomState, filePath, uploadName string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open recording for upload: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat recording for upload: %w", err)
	}
	if uploadName == "" {
		uploadName = filepath.Base(filePath)
	}

	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentLength, err := talkUploadContentLength(writer.Boundary(), state.Owner, uploadName, info.Size())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newUploadActivity()
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go watchUploadActivity(activity, rt.talkUploadStall, cancel, watchdogDone)

	go func() {
		pipeWriter.CloseWithError(streamTalkUpload(writer, state.Owner, uploadName, file))
	}()

	random := talkRandom()
	endpoint := strings.TrimRight(state.BackendURL, "/") + "/ocs/v2.php/apps/spreed/api/v1/recording/" + state.RoomToken + "/store"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &uploadActivityReader{reader: pipeReader, activity: activity})
	if err != nil {
		return fmt.Errorf("build recording upload request: %w", err)
	}
	req.ContentLength = contentLength
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("OCS-ApiRequest", "true")
	req.Header.Set(talkRecordingRandomHeader, random)
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, random, []byte(state.RoomToken)))
	return rt.doTalkRequest(rt.talkUploadClient, req)
}

// streamTalkUpload writes the multipart upload body; it runs in a goroutine
// feeding the request pipe and must produce exactly the bytes
// talkUploadContentLength accounted for.
func streamTalkUpload(writer *multipart.Writer, owner, uploadName string, file io.Reader) error {
	if err := writer.WriteField("owner", owner); err != nil {
		return fmt.Errorf("write upload owner field: %w", err)
	}
	part, err := writer.CreateFormFile("file", uploadName)
	if err != nil {
		return fmt.Errorf("create upload form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy upload file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}
	return nil
}

// talkUploadContentLength precomputes the multipart body size: the framing
// around an empty file part (using the same boundary and field names as the
// streamed body) plus the file size.
func talkUploadContentLength(boundary, owner, uploadName string, fileSize int64) (int64, error) {
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	if err := writer.SetBoundary(boundary); err != nil {
		return 0, fmt.Errorf("set upload boundary: %w", err)
	}
	if err := streamTalkUpload(writer, owner, uploadName, strings.NewReader("")); err != nil {
		return 0, err
	}
	return int64(framing.Len()) + fileSize, nil
}

// uploadActivity tracks progress of the streamed upload body so the watchdog
// can tell a slow-but-moving upload from a stalled connection.
type uploadActivity struct {
	lastReadNS atomic.Int64
	bodyDone   atomic.Bool
}

func newUploadActivity() *uploadActivity {
	activity := &uploadActivity{}
	activity.touch()
	return activity
}

func (a *uploadActivity) touch() {
	a.lastReadNS.Store(time.Now().UnixNano())
}

func (a *uploadActivity) idleFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - a.lastReadNS.Load())
}

// uploadActivityReader is the request body: the transport reads from it as
// fast as the connection drains, so successful reads are a progress signal.
type uploadActivityReader struct {
	reader   io.Reader
	activity *uploadActivity
}

func (r *uploadActivityReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.activity.touch()
	}
	if errors.Is(err, io.EOF) {
		r.activity.bodyDone.Store(true)
	}
	return n, err
}

// watchUploadActivity cancels a stalled upload: no body progress for the
// stall grace while the body is still being streamed. After the body is
// fully written it exits and leaves the response wait to the client's
// ResponseHeaderTimeout.
func watchUploadActivity(activity *uploadActivity, stall time.Duration, cancel context.CancelFunc, done <-chan struct{}) {
	if stall <= 0 {
		return
	}
	ticker := time.NewTicker(stall / 4)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if activity.bodyDone.Load() {
				return
			}
			if activity.idleFor() >= stall {
				cancel()
				return
			}
		}
	}
}

// talkHTTPError is a non-2xx response from Nextcloud; only 5xx responses are
// worth retrying.
type talkHTTPError struct {
	Status int
	Body   string
}

func (e *talkHTTPError) Error() string {
	return fmt.Sprintf("talk request failed: HTTP %d: %s", e.Status, e.Body)
}

func isRetryableTalkError(err error) bool {
	var httpErr *talkHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status >= 500
	}
	// Transport-level failures (timeouts, refused/stalled connections) are
	// retryable; anything else (local file errors, request building) is not.
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// withTalkRetry runs fn with the runtime's bounded backoff schedule. 4xx
// responses are definitive and returned immediately.
func (rt *Runtime) withTalkRetry(jobID, op string, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt >= len(rt.talkRetryDelays) || !isRetryableTalkError(err) {
			return err
		}
		delay := rt.talkRetryDelays[attempt]
		rt.logger.Printf("talk %s failed id=%s (attempt %d/%d): %v; retrying in %s", op, jobID, attempt+1, len(rt.talkRetryDelays)+1, err, delay)
		time.Sleep(delay)
	}
}

func (rt *Runtime) doTalkRequest(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("talk request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return &talkHTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return nil
}

// talkJobBinding is the durable subset of talkRoomState persisted on the
// jobs row, so an operator restart mid-recording can still clean up the
// spreed room state and a rerun can re-deliver the recording (D-352). The
// in-memory binding alone meant a restart left the room stuck "recording"
// and orphaned the upload forever.
type talkJobBinding struct {
	BackendURL string         `json:"backend_url"`
	RoomToken  string         `json:"room_token"`
	Owner      string         `json:"owner"`
	RoomName   string         `json:"room_name,omitempty"`
	Status     int            `json:"status,omitempty"`
	StartActor *talkActorData `json:"start_actor,omitempty"`
}

func encodeTalkBinding(state *talkRoomState) (string, error) {
	body, err := json.Marshal(talkJobBinding{
		BackendURL: state.BackendURL,
		RoomToken:  state.RoomToken,
		Owner:      state.Owner,
		RoomName:   state.RoomName,
		Status:     state.Status,
		StartActor: state.StartActor,
	})
	if err != nil {
		return "", fmt.Errorf("encode talk binding: %w", err)
	}
	return string(body), nil
}

func decodeTalkBinding(raw string) (talkRoomState, error) {
	var binding talkJobBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return talkRoomState{}, fmt.Errorf("decode talk binding: %w", err)
	}
	if strings.TrimSpace(binding.BackendURL) == "" || strings.TrimSpace(binding.RoomToken) == "" {
		return talkRoomState{}, errors.New("talk binding is missing backend URL or room token")
	}
	return talkRoomState{
		BackendURL: binding.BackendURL,
		RoomToken:  binding.RoomToken,
		Owner:      binding.Owner,
		RoomName:   binding.RoomName,
		Status:     binding.Status,
		StartActor: binding.StartActor,
	}, nil
}

// deliverTalkRecording sends spreed the stopped callback and uploads the
// recording, both with bounded retries. Failures are logged but never fail
// the record stage: the recording is already safe in the canonical run
// bundle, the pipeline continues to build/publish, and a rerun re-attempts
// delivery while talk_delivered_at is unset (D-352).
func (rt *Runtime) deliverTalkRecording(jobID string, state talkRoomState, runPath, finishedAt string) {
	if err := rt.withTalkRetry(jobID, "stopped callback", func() error {
		return rt.notifyTalkStopped(state)
	}); err != nil {
		rt.logger.Printf("talk stopped callback failed id=%s: %v (rerun re-attempts delivery)", jobID, err)
		return
	}
	rt.uploadAndMarkTalkDelivered(jobID, state, runPath, finishedAt)
}

func (rt *Runtime) uploadAndMarkTalkDelivered(jobID string, state talkRoomState, runPath, finishedAt string) {
	recordingPath := filepath.Join(runPath, "recording.mkv")
	uploadName := talkRecordingUploadName(finishedAt)
	if err := rt.withTalkRetry(jobID, "recording upload", func() error {
		return rt.uploadTalkRecording(state, recordingPath, uploadName)
	}); err != nil {
		rt.logger.Printf("talk upload failed id=%s recording=%s: %v (rerun re-attempts delivery)", jobID, recordingPath, err)
		return
	}
	if err := rt.store.MarkTalkDelivered(context.Background(), jobID, nowUTCString()); err != nil {
		rt.logger.Printf("talk delivered update failed id=%s: %v", jobID, err)
	}
}

// redeliverTalkRecording re-runs Talk delivery during a rerun of a job whose
// recording never reached Nextcloud (delivery failure or operator restart).
// The stopped callback is best-effort here — spreed's room state has usually
// been cleaned up already by the startup failed callback or a moderator stop
// — the upload is what matters.
func (rt *Runtime) redeliverTalkRecording(job Job) {
	if job.TalkBinding == nil || job.TalkDeliveredAt != nil {
		return
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		rt.logger.Printf("talk redelivery skipped id=%s: %v", job.ID, err)
		return
	}
	if job.ArtifactRunPath == nil || strings.TrimSpace(*job.ArtifactRunPath) == "" {
		return
	}
	runPath := *job.ArtifactRunPath
	if _, err := os.Stat(filepath.Join(runPath, "recording.mkv")); err != nil {
		rt.logger.Printf("talk redelivery skipped id=%s: %v", job.ID, err)
		return
	}
	// The stopped callback is room-scoped: if a NEWER recording is live in the
	// same room, sending it would mark the live recording stopped in spreed
	// while its recorder keeps running. Re-check before every attempt — the
	// retry backoff is long enough for a moderator to start a fresh recording
	// mid-loop. The upload below still proceeds: the store endpoint only files
	// the recording into the owner's storage and never touches room state.
	if err := rt.withTalkRetry(job.ID, "stopped callback", func() error {
		if liveJobID, live := rt.talkRoomLiveWithOtherJob(state.RoomToken, job.ID); live {
			return fmt.Errorf("%w: job %s is recording in room %s", errTalkRoomLive, liveJobID, state.RoomToken)
		}
		return rt.notifyTalkStopped(state)
	}); err != nil {
		if errors.Is(err, errTalkRoomLive) {
			rt.logger.Printf("talk stopped callback skipped during redelivery id=%s room=%s: %v", job.ID, state.RoomToken, err)
		} else {
			rt.logger.Printf("talk stopped callback failed during redelivery id=%s: %v", job.ID, err)
		}
	}
	finishedAt := ""
	if job.RecordFinishedAt != nil {
		finishedAt = *job.RecordFinishedAt
	}
	rt.uploadAndMarkTalkDelivered(job.ID, state, runPath, finishedAt)
}

// NotifyInterruptedTalkRecordings tells spreed that recordings interrupted
// by an operator restart failed. Without it the room stays "recording"
// forever, surfacing only as RECORDING_FAILED when a moderator eventually
// clicks stop (D-352/D-362). Only jobs interrupted by the current startup
// sweep are notified, so older history cannot clobber a newer recording in
// the same room.
func (rt *Runtime) NotifyInterruptedTalkRecordings(interruptedAt string) {
	jobs, err := rt.store.ListInterruptedTalkRecordJobs(context.Background(), interruptedAt)
	if err != nil {
		rt.logger.Printf("list interrupted talk jobs failed: %v", err)
		return
	}
	for _, job := range jobs {
		state, err := decodeTalkBinding(job.Binding)
		if err != nil {
			rt.logger.Printf("talk failed callback skipped id=%s: %v", job.ID, err)
			continue
		}
		// The failed callback is room-scoped: this goroutine can lag minutes
		// behind startup (each earlier job may burn the full retry schedule),
		// long enough for a moderator to restart recording in the same room.
		// Re-check the live binding before every attempt so a stale callback
		// never clobbers the new recording's status in spreed (D-352).
		err = rt.withTalkRetry(job.ID, "failed callback", func() error {
			if liveJobID, live := rt.talkRoomLiveWithOtherJob(state.RoomToken, job.ID); live {
				return fmt.Errorf("%w: job %s is recording in room %s", errTalkRoomLive, liveJobID, state.RoomToken)
			}
			return rt.notifyTalkFailed(state)
		})
		switch {
		case errors.Is(err, errTalkRoomLive):
			rt.logger.Printf("talk failed callback skipped for interrupted job id=%s room=%s: %v", job.ID, state.RoomToken, err)
		case err != nil:
			rt.logger.Printf("talk failed callback for interrupted job failed id=%s room=%s: %v", job.ID, state.RoomToken, err)
		default:
			rt.logger.Printf("talk failed callback sent for interrupted job id=%s room=%s", job.ID, state.RoomToken)
		}
	}
}
