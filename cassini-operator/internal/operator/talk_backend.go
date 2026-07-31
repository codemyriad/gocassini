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
	"net/http"
	"net/url"
	"strings"
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

// Every request to Nextcloud must be bounded: one hung connection through
// http.DefaultClient used to wedge all recording capacity until restart
// (D-352). The record slot itself is released before these callbacks run
// (run.go), but a hung callback still delays the build enqueue behind it.
const (
	// talkJSONRequestTimeout bounds the small JSON callbacks
	// (started/stopped/failed) end to end.
	talkJSONRequestTimeout = 30 * time.Second
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
	return rt.doTalkRequest(req)
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

func (rt *Runtime) doTalkRequest(req *http.Request) error {
	resp, err := rt.talkJSONClient.Do(req)
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
// spreed room state (D-352). The in-memory binding alone meant a restart left
// the room stuck "recording" forever. It is also what publish reads to resolve
// the room's participants and display name long after the recorder is gone
// (talk_participants.go, talk_room_name.go).
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

// reportTalkRecordingStopped tells spreed the recording finished, with bounded
// retries. Failure is logged but never fails the record stage: the recording is
// already safe in the canonical run bundle and the pipeline continues to
// build/publish (D-352).
//
// Cassini deliberately sends spreed *status only*. The meeting itself reaches
// Nextcloud exactly once, as the published .opus under the canonical recordings
// root — never through Talk's recording store (D-551).
//
// Marking talk_stopped_at here is what keeps the startup sweep honest: a room
// spreed was already told "stopped" about must never later be told "failed".
func (rt *Runtime) reportTalkRecordingStopped(jobID string, state talkRoomState) {
	if err := rt.withTalkRetry(jobID, "stopped callback", func() error {
		return rt.notifyTalkStopped(state)
	}); err != nil {
		// No replay path exists for this: the startup sweep only covers jobs
		// left at stage='record', state='interrupted', and a job that finishes
		// cleanly is never in that set. The room's status is then recovered by
		// a moderator clicking stop (D-551 followups: a durable outbox).
		rt.logger.Printf("talk stopped callback failed id=%s: %v (room status left to the moderator's stop)", jobID, err)
		return
	}
	if err := rt.store.MarkTalkStopped(context.Background(), jobID, nowUTCString()); err != nil {
		rt.logger.Printf("talk stopped marker update failed id=%s: %v", jobID, err)
	}
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
