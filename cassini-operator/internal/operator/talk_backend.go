package operator

import (
	"bytes"
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
	"os"
	"path/filepath"
	"strings"
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
		if err := rt.runRecordDoctor(); err != nil {
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
	if _, exists := rt.lookupTalkRoomState(roomKey); exists {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	req := TriggerRequest{
		Platform:              nextcloudTalkProvider,
		BaseURL:               publicBaseURL,
		TalkConnectURL:        operatorBaseURL,
		RoomToken:             token,
		GuestName:             defaultGuestName,
		StopWhenRoomEmpty:     true,
		RoomEmptyGraceSeconds: defaultRoomEmptySec,
	}
	requestBody, err := encodeTriggerRequest(req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp, err := rt.acceptRecordJob(r.Context(), nextcloudTalkProvider, requestBody, req)
	if err != nil {
		status, msg := recordAcceptError(err)
		writeJSONError(w, status, msg)
		return
	}
	rt.bindTalkRoomState(&talkRoomState{
		RoomKey:    roomKey,
		JobID:      resp.ID,
		BackendURL: operatorBaseURL,
		RoomToken:  token,
		Owner:      strings.TrimSpace(payload.Start.Owner),
		Status:     payload.Start.Status,
		StartActor: payload.Start.Actor,
	})
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

func (rt *Runtime) bindTalkRoomState(state *talkRoomState) {
	rt.recordMu.Lock()
	rt.talkRooms[state.RoomKey] = state
	rt.talkJobs[state.JobID] = state
	rt.recordMu.Unlock()
}

func (rt *Runtime) lookupTalkRoomState(roomKey string) (*talkRoomState, bool) {
	rt.recordMu.Lock()
	state, ok := rt.talkRooms[roomKey]
	rt.recordMu.Unlock()
	return state, ok
}

func (rt *Runtime) lookupTalkJobState(jobID string) (*talkRoomState, bool) {
	rt.recordMu.Lock()
	state, ok := rt.talkJobs[jobID]
	rt.recordMu.Unlock()
	return state, ok
}

func (rt *Runtime) updateTalkStopActor(jobID string, actor *talkActorData) {
	rt.recordMu.Lock()
	if state, ok := rt.talkJobs[jobID]; ok {
		state.StopActor = actor
	}
	rt.recordMu.Unlock()
}

func (rt *Runtime) clearTalkRoomJobByID(jobID string) {
	rt.recordMu.Lock()
	if state, ok := rt.talkJobs[jobID]; ok {
		delete(rt.talkRooms, state.RoomKey)
		delete(rt.talkJobs, jobID)
	}
	rt.recordMu.Unlock()
}

func (rt *Runtime) notifyTalkStarted(state *talkRoomState) error {
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

func (rt *Runtime) notifyTalkStopped(state *talkRoomState) error {
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

func (rt *Runtime) notifyTalkFailed(state *talkRoomState) error {
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

func (rt *Runtime) uploadTalkRecording(state *talkRoomState, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open recording for upload: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("owner", state.Owner); err != nil {
		return fmt.Errorf("write upload owner field: %w", err)
	}
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("create upload form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy upload file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	random := talkRandom()
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(state.BackendURL, "/")+"/ocs/v2.php/apps/spreed/api/v1/recording/"+state.RoomToken+"/store", &body)
	if err != nil {
		return fmt.Errorf("build recording upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("OCS-ApiRequest", "true")
	req.Header.Set(talkRecordingRandomHeader, random)
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, random, []byte(state.RoomToken)))
	return rt.doTalkRequest(req)
}

func (rt *Runtime) doTalkRequest(req *http.Request) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("talk request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("talk request failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
