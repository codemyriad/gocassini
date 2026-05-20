package operator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestTalkWelcomeHandlerReturnsVersion(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/welcome", nil)
	rec := httptest.NewRecorder()
	rt.talkWelcomeHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"version":1}` {
		t.Fatalf("body = %q, want %q", got, `{"version":1}`)
	}
}

func TestTalkWelcomeHandlerRejectsNonGET(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/welcome", nil)
	rec := httptest.NewRecorder()
	rt.talkWelcomeHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusMethodNotAllowed, rec.Body.String())
	}
}

func TestHealthzHandlerShallow(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	rt.healthzHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"ok":true,"check":"shallow","version":1}` {
		t.Fatalf("body = %q", got)
	}
}

func TestHealthzHandlerRecordCheck(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/healthz?check=record", nil)
	rec := httptest.NewRecorder()
	rt.healthzHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "doctor --target record") {
		t.Fatalf("expected doctor invocation, got %s", logText)
	}
}

func TestHealthzHandlerRecordCheckFailure(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()
	t.Setenv("FAKE_CASSINI_DOCTOR_FAIL", "1")

	req := httptest.NewRequest(http.MethodGet, "/healthz?check=record", nil)
	rec := httptest.NewRecorder()
	rt.healthzHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestHealthzHandlerRejectsUnknownCheck(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/healthz?check=nope", nil)
	rec := httptest.NewRecorder()
	rt.healthzHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestValidateTalkRequest(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"

	body := []byte(`{"type":"start"}`)
	random := "random-seed"
	checksum := talkChecksum(rt.cfg.TalkSharedSecret, random, body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/token", strings.NewReader(string(body)))
	req.Header.Set(talkRecordingBackendHeader, "https://cloud.example.test")
	req.Header.Set(talkRecordingRandomHeader, random)
	req.Header.Set(talkRecordingChecksumHeader, checksum)

	auth, err := rt.validateTalkRequest(req, body)
	if err != nil {
		t.Fatalf("validateTalkRequest() error = %v", err)
	}
	if auth.BackendURL != "https://cloud.example.test" {
		t.Fatalf("backend url = %q", auth.BackendURL)
	}
}

func TestValidateTalkRequestRejectsMissingSecret(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/token", strings.NewReader(`{}`))
	_, err := rt.validateTalkRequest(req, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "talk shared secret is not configured") {
		t.Fatalf("expected missing secret error, got %v", err)
	}
}

func TestValidateTalkRequestRejectsInvalidChecksum(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"

	body := []byte(`{"type":"start"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/token", strings.NewReader(string(body)))
	req.Header.Set(talkRecordingBackendHeader, "https://cloud.example.test")
	req.Header.Set(talkRecordingRandomHeader, "random-seed")
	req.Header.Set(talkRecordingChecksumHeader, "bad")

	_, err := rt.validateTalkRequest(req, body)
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestTalkRoomStartAcceptsAuthenticatedRequest(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	reqBody := `{"type":"start","start":{"owner":"chima","actor":{"type":"users","id":"chima"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(reqBody))
	req.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	req.Header.Set(talkRecordingRandomHeader, "random-seed")
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-seed", []byte(reqBody)))
	rec := httptest.NewRecorder()
	rt.talkRoomHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	jobs, err := rt.store.ListJobs(req.Context())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	_ = waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "record --call "+fakeTalk.server.URL+"/call/room123") {
		t.Fatalf("expected synthesized call URL in log, got %s", logText)
	}
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertUploadCount(t, 1)
}

func TestTalkRoomStartUsesConfiguredBackendURLForOperatorCalls(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	rt.cfg.TalkBackendURL = fakeTalk.server.URL

	reqBody := `{"type":"start","start":{"owner":"chima","actor":{"type":"users","id":"chima"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(reqBody))
	req.Header.Set(talkRecordingBackendHeader, "http://localhost:28080")
	req.Header.Set(talkRecordingRandomHeader, "random-seed")
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-seed", []byte(reqBody)))
	rec := httptest.NewRecorder()
	rt.talkRoomHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	jobs, err := rt.store.ListJobs(req.Context())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	_ = waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "record --call http://localhost:28080/call/room123") {
		t.Fatalf("expected public synthesized call URL in log, got %s", logText)
	}
	if !strings.Contains(logText, "--connect-url "+fakeTalk.server.URL) {
		t.Fatalf("expected operator connect URL in log, got %s", logText)
	}
	if !strings.Contains(jobs[0].RequestJSON, `"baseURL":"http://localhost:28080"`) {
		t.Fatalf("expected public base URL in request JSON, got %s", jobs[0].RequestJSON)
	}
	if !strings.Contains(jobs[0].RequestJSON, `"talkConnectURL":"`+fakeTalk.server.URL+`"`) {
		t.Fatalf("expected connect URL in request JSON, got %s", jobs[0].RequestJSON)
	}
	if _, ok := rt.lookupTalkRoomState(talkRoomKey("http://localhost:28080", "room123")); ok {
		t.Fatalf("expected public room mapping to be cleared after completion")
	}
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertUploadCount(t, 1)
}

func TestTalkRoomStartIsIdempotentForKnownRoom(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	rt.bindTalkRoomState(&talkRoomState{
		RoomKey:    talkRoomKey(fakeTalk.server.URL, "room123"),
		JobID:      "job-1",
		BackendURL: fakeTalk.server.URL,
		RoomToken:  "room123",
		Owner:      "chima",
	})

	reqBody := `{"type":"start","start":{"owner":"chima","actor":{"type":"users","id":"chima"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(reqBody))
	req.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	req.Header.Set(talkRecordingRandomHeader, "random-seed")
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-seed", []byte(reqBody)))
	rec := httptest.NewRecorder()
	rt.talkRoomHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	jobs, err := rt.store.ListJobs(req.Context())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no new jobs, got %d", len(jobs))
	}
}

func TestTalkRoomStopAcceptsAuthenticatedRequest(t *testing.T) {
	rt, cleanup, _, startedPath := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	t.Setenv("FAKE_RECORD_WAIT_FOR_SIGNAL", "1")
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	startBody := `{"type":"start","start":{"owner":"chima","actor":{"type":"users","id":"chima"}}}`
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(startBody))
	startReq.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	startReq.Header.Set(talkRecordingRandomHeader, "random-start")
	startReq.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-start", []byte(startBody)))
	startRec := httptest.NewRecorder()
	rt.talkRoomHandler(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d body=%s", startRec.Code, http.StatusOK, startRec.Body.String())
	}
	waitForFile(t, startedPath)
	state, ok := rt.lookupTalkRoomState(talkRoomKey(fakeTalk.server.URL, "room123"))
	if !ok {
		t.Fatalf("expected room mapping")
	}
	waitForRecordState(t, rt.store, state.JobID, "running")

	stopBody := `{"type":"stop","stop":{"actor":{"type":"users","id":"chima"}}}`
	stopReq := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(stopBody))
	stopReq.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	stopReq.Header.Set(talkRecordingRandomHeader, "random-stop")
	stopReq.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-stop", []byte(stopBody)))
	stopRec := httptest.NewRecorder()
	rt.talkRoomHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, state.JobID, "succeeded")
	if _, ok := rt.lookupTalkRoomState(talkRoomKey(fakeTalk.server.URL, "room123")); ok {
		t.Fatalf("expected room mapping to be cleared after completion")
	}
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertUploadCount(t, 1)
}

func TestTalkRoomStopAcceptsEmptyArrayPayload(t *testing.T) {
	rt, cleanup, _, startedPath := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	t.Setenv("FAKE_RECORD_WAIT_FOR_SIGNAL", "1")
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	startBody := `{"type":"start","start":{"owner":"chima","actor":{"type":"users","id":"chima"}}}`
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(startBody))
	startReq.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	startReq.Header.Set(talkRecordingRandomHeader, "random-start")
	startReq.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-start", []byte(startBody)))
	startRec := httptest.NewRecorder()
	rt.talkRoomHandler(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d body=%s", startRec.Code, http.StatusOK, startRec.Body.String())
	}
	waitForFile(t, startedPath)
	state, ok := rt.lookupTalkRoomState(talkRoomKey(fakeTalk.server.URL, "room123"))
	if !ok {
		t.Fatalf("expected room mapping")
	}
	waitForRecordState(t, rt.store, state.JobID, "running")

	stopBody := `{"type":"stop","stop":[]}`
	stopReq := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(stopBody))
	stopReq.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	stopReq.Header.Set(talkRecordingRandomHeader, "random-stop")
	stopReq.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-stop", []byte(stopBody)))
	stopRec := httptest.NewRecorder()
	rt.talkRoomHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, state.JobID, "succeeded")
	if _, ok := rt.lookupTalkRoomState(talkRoomKey(fakeTalk.server.URL, "room123")); ok {
		t.Fatalf("expected room mapping to be cleared after completion")
	}
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertUploadCount(t, 1)
}

func TestTalkRoomStopReturnsNotFoundForUnknownRoom(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	stopBody := `{"type":"stop","stop":{"actor":{"type":"users","id":"chima"}}}`
	stopReq := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(stopBody))
	stopReq.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	stopReq.Header.Set(talkRecordingRandomHeader, "random-stop")
	stopReq.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-stop", []byte(stopBody)))
	stopRec := httptest.NewRecorder()
	rt.talkRoomHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusNotFound {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusNotFound, stopRec.Body.String())
	}
}

func TestTalkRoomStartFailureSendsFailedCallback(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	rt.recordJobFn = func(_ context.Context, _ Job, _ TriggerRequest) (recordResult, error) {
		return recordResult{}, assertErr("boom")
	}

	reqBody := `{"type":"start","start":{"owner":"chima","actor":{"type":"users","id":"chima"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/room/room123", strings.NewReader(reqBody))
	req.Header.Set(talkRecordingBackendHeader, fakeTalk.server.URL)
	req.Header.Set(talkRecordingRandomHeader, "random-seed")
	req.Header.Set(talkRecordingChecksumHeader, talkChecksum(rt.cfg.TalkSharedSecret, "random-seed", []byte(reqBody)))
	rec := httptest.NewRecorder()
	rt.talkRoomHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	jobs, err := rt.store.ListJobs(req.Context())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	_ = waitForJobState(t, rt.store, jobs[0].ID, "failed")
	fakeTalk.assertEventTypes(t, []string{"failed"})
}

type fakeTalkServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	events  []string
	uploads int
}

func newFakeTalkServer(t *testing.T) *fakeTalkServer {
	t.Helper()
	ft := &fakeTalkServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ocs/v2.php/apps/spreed/api/v1/recording/backend", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(talkRecordingRandomHeader); len(got) < 32 {
			t.Fatalf("%s length = %d, want >= 32", talkRecordingRandomHeader, len(got))
		}
		if r.Header.Get(talkRecordingRandomHeader) == "" {
			t.Fatalf("missing %s header", talkRecordingRandomHeader)
		}
		if r.Header.Get(talkRecordingChecksumHeader) == "" {
			t.Fatalf("missing %s header", talkRecordingChecksumHeader)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode backend payload: %v", err)
		}
		eventType, _ := payload["type"].(string)
		ft.mu.Lock()
		ft.events = append(ft.events, eventType)
		ft.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`))
	})
	mux.HandleFunc("/ocs/v2.php/apps/spreed/api/v1/recording/room123/store", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(talkRecordingRandomHeader); len(got) < 32 {
			t.Fatalf("%s length = %d, want >= 32", talkRecordingRandomHeader, len(got))
		}
		if r.Header.Get(talkRecordingRandomHeader) == "" {
			t.Fatalf("missing %s header", talkRecordingRandomHeader)
		}
		if r.Header.Get(talkRecordingChecksumHeader) == "" {
			t.Fatalf("missing %s header", talkRecordingChecksumHeader)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("owner"); got != "chima" {
			t.Fatalf("owner = %q, want chima", got)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		_, _ = io.ReadAll(file)
		_ = file.Close()
		ft.mu.Lock()
		ft.uploads++
		ft.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`))
	})
	ft.server = httptest.NewServer(mux)
	return ft
}

func (f *fakeTalkServer) Close() {
	f.server.Close()
}

func (f *fakeTalkServer) assertEventTypes(t *testing.T, want []string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) != len(want) {
		t.Fatalf("events = %#v, want %#v", f.events, want)
	}
	for i := range want {
		if f.events[i] != want[i] {
			t.Fatalf("events = %#v, want %#v", f.events, want)
		}
	}
}

func (f *fakeTalkServer) assertUploadCount(t *testing.T, want int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploads != want {
		t.Fatalf("uploads = %d, want %d", f.uploads, want)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
