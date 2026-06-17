package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	if !strings.Contains(logText, "--call "+fakeTalk.server.URL+"/call/room123") {
		t.Fatalf("expected synthesized call URL in log, got %s", logText)
	}
	if !strings.Contains(logText, "--talk-auth-mode hpb-internal") {
		t.Fatalf("expected talk auth mode flag in log, got %s", logText)
	}
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertUploadCount(t, 1)
	fakeTalk.assertUploadFilenames(t)
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
	if !strings.Contains(logText, "--call http://localhost:28080/call/room123") {
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
	if !strings.Contains(jobs[0].RequestJSON, `"talkAuthMode":"hpb-internal"`) {
		t.Fatalf("expected talk auth mode in request JSON, got %s", jobs[0].RequestJSON)
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
	state := &talkRoomState{
		RoomKey:    talkRoomKey(fakeTalk.server.URL, "room123"),
		BackendURL: fakeTalk.server.URL,
		RoomToken:  "room123",
		Owner:      "chima",
	}
	if !rt.reserveTalkRoom(state) {
		t.Fatalf("expected to reserve fresh room state")
	}
	rt.bindTalkRoomJob(state, "job-1")

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

func TestTalkRoomStartClearsRoomStateWhenJobFailsInstantly(t *testing.T) {
	// Regression for D-364: the room binding used to be created after the
	// record goroutine was spawned, so a job that failed before the bind left
	// a permanently stale "already recording" entry. The binding now happens
	// before the goroutine starts, so its deferred cleanup always finds it.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	rt.recordJobFn = func(_ context.Context, _ Job, _ TriggerRequest) (recordResult, error) {
		return recordResult{}, assertErr("instant failure")
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
	roomKey := talkRoomKey(fakeTalk.server.URL, "room123")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := rt.lookupTalkRoomState(roomKey); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected stale room binding to be cleared after instant job failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fakeTalk.assertEventTypes(t, []string{"failed"})
}

func TestTalkDeliveryRetriesTransientUploadFailure(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{10 * time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	fakeTalk.setStoreFailures(1)

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
	job := waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	if job.TalkBinding == nil || !strings.Contains(*job.TalkBinding, `"room_token":"room123"`) {
		t.Fatalf("expected persisted talk binding, got %#v", job.TalkBinding)
	}
	if job.TalkDeliveredAt == nil {
		t.Fatalf("expected talk_delivered_at after retried upload, got nil")
	}
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertUploadCount(t, 1)
	if got := fakeTalk.storeRequestCount(); got != 2 {
		t.Fatalf("store requests = %d, want 2 (one 500 + one retry)", got)
	}
}

func TestTalkDeliveryFailureKeepsPipelineAndRerunRedelivers(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	fakeTalk.setStoreFailures(-1)

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

	// A fully recorded job must not fail because Nextcloud rejected the
	// upload: the pipeline continues to build/publish, the recording stays in
	// the canonical run bundle, and talk_delivered_at stays unset.
	job := waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	if job.Stage != "done" {
		t.Fatalf("stage = %q, want done", job.Stage)
	}
	if job.TalkDeliveredAt != nil {
		t.Fatalf("expected talk_delivered_at to stay unset after failed delivery, got %#v", job.TalkDeliveredAt)
	}
	if got := fakeTalk.storeRequestCount(); got != 2 {
		t.Fatalf("store requests = %d, want 2 (initial + one retry)", got)
	}
	fakeTalk.assertUploadCount(t, 0)

	// Rerun re-delivers using the persisted binding once Nextcloud recovers.
	fakeTalk.setStoreFailures(0)
	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+job.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current := mustGetJob(t, rt.store, job.ID)
		if current.TalkDeliveredAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected rerun to redeliver the recording, job = %#v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = waitForJobState(t, rt.store, job.ID, "succeeded")
	fakeTalk.assertUploadCount(t, 1)
}

func TestTalkStoppedCallbackTimeoutDoesNotWedgeJob(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
	rt.talkJSONClient = &http.Client{Timeout: 50 * time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()
	fakeTalk.setStoppedDelay(300 * time.Millisecond)

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

	// The stopped callback hangs server-side; the bounded client + retries
	// must give up and let the pipeline finish instead of wedging the only
	// record slot. Delivery stays incomplete for rerun to pick up.
	job := waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	if job.TalkDeliveredAt != nil {
		t.Fatalf("expected incomplete delivery after stopped-callback timeouts, got %#v", job.TalkDeliveredAt)
	}
	fakeTalk.assertUploadCount(t, 0)
}

func TestUploadTalkRecordingCancelsStalledUpload(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.talkUploadStall = 100 * time.Millisecond

	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never read the request body: the upload stalls once the socket
		// buffers fill, and the watchdog must cancel it.
		<-release
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })

	recordingPath := filepath.Join(t.TempDir(), "recording.mkv")
	if err := os.WriteFile(recordingPath, bytes.Repeat([]byte("x"), 8<<20), 0o644); err != nil {
		t.Fatalf("write fixture recording: %v", err)
	}

	start := time.Now()
	err := rt.uploadTalkRecording(talkRoomState{
		BackendURL: server.URL,
		RoomToken:  "room123",
		Owner:      "chima",
	}, recordingPath, "recording.mkv")
	once.Do(func() { close(release) })
	if err == nil {
		t.Fatal("expected stalled upload to fail")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stalled upload took %s to cancel, want well under the test deadline", elapsed)
	}
}

func TestTalkUploadContentLengthMatchesEncodedBody(t *testing.T) {
	const owner = "chima"
	const uploadName = "recording-20260612T000000.000000000Z.mkv"
	payload := "0123456789abcdef"

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := streamTalkUpload(writer, owner, uploadName, strings.NewReader(payload)); err != nil {
		t.Fatalf("streamTalkUpload() error = %v", err)
	}
	got, err := talkUploadContentLength(writer.Boundary(), owner, uploadName, int64(len(payload)))
	if err != nil {
		t.Fatalf("talkUploadContentLength() error = %v", err)
	}
	if want := int64(body.Len()); got != want {
		t.Fatalf("content length = %d, want %d", got, want)
	}
}

func TestNotifyInterruptedTalkRecordingsSendsFailedCallback(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	// A recording was running when the operator died: the job row survives
	// with its persisted Talk binding, spreed still believes the room is
	// recording.
	seedJobRow(t, rt.store.db, seededJobRow{ID: "talk-interrupted", Stage: "record", State: "running", CreatedAt: "2026-06-12T10:00:00Z"})
	binding := `{"backend_url":"` + fakeTalk.server.URL + `","room_token":"room123","owner":"chima"}`
	if err := rt.store.SetJobTalkBinding(context.Background(), "talk-interrupted", binding); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}

	interruptedAt := nowUTCString()
	count, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), interruptedAt)
	if err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("interrupted count = %d, want 1", count)
	}

	rt.NotifyInterruptedTalkRecordings(interruptedAt)
	fakeTalk.assertEventTypes(t, []string{"failed"})
}

func TestNotifyInterruptedTalkRecordingsNotifiesEachInterruptionOnce(t *testing.T) {
	// Regression: the startup sweep used to re-stamp already-interrupted jobs
	// (state NOT IN succeeded/failed matches 'interrupted'), so a Talk job
	// interrupted by crash epoch 1 that was never rerun got a fresh
	// interrupted_at on every restart and spreed received the same failed
	// callback forever. Two consecutive startups must yield exactly one
	// callback.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	seedJobRow(t, rt.store.db, seededJobRow{ID: "talk-interrupted", Stage: "record", State: "running", CreatedAt: "2026-06-12T10:00:00Z"})
	binding := `{"backend_url":"` + fakeTalk.server.URL + `","room_token":"room123","owner":"chima"}`
	if err := rt.store.SetJobTalkBinding(context.Background(), "talk-interrupted", binding); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}

	// First startup after the crash: the sweep stamps the job and the notify
	// pass sends spreed exactly one failed callback.
	firstSweep := "2026-06-12T11:00:00Z"
	count, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), firstSweep)
	if err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("first sweep count = %d, want 1", count)
	}
	rt.NotifyInterruptedTalkRecordings(firstSweep)
	fakeTalk.assertEventTypes(t, []string{"failed"})

	// Second startup: the job is still interrupted (a mid-recording Talk job
	// has no canonical run, so it is never rerun). The sweep must not
	// re-stamp it, and the notify pass must not re-send the callback.
	secondSweep := "2026-06-12T12:00:00Z"
	count, err = rt.store.MarkIncompleteJobsInterrupted(context.Background(), secondSweep)
	if err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("second sweep count = %d, want 0 (interrupted jobs must not be re-stamped)", count)
	}
	job := mustGetJob(t, rt.store, "talk-interrupted")
	if job.InterruptedAt == nil || *job.InterruptedAt != firstSweep {
		t.Fatalf("interrupted_at = %#v, want first sweep epoch %q", job.InterruptedAt, firstSweep)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), "talk-interrupted")
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].InterruptedAt == nil || *attempts[0].InterruptedAt != firstSweep {
		t.Fatalf("attempt interrupted_at not preserved at first sweep epoch, got %#v", attempts)
	}
	rt.NotifyInterruptedTalkRecordings(secondSweep)
	fakeTalk.assertEventTypes(t, []string{"failed"})
}

func TestNotifyInterruptedTalkRecordingsSkipsRoomWithLiveRecording(t *testing.T) {
	// Regression: the failed callback is keyed only by room token, so sending
	// it while a NEW recording is live in the same room would mark the live
	// recording failed in spreed (D-352).
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	seedJobRow(t, rt.store.db, seededJobRow{ID: "talk-interrupted", Stage: "record", State: "running", CreatedAt: "2026-06-12T10:00:00Z"})
	binding := `{"backend_url":"` + fakeTalk.server.URL + `","room_token":"room123","owner":"chima"}`
	if err := rt.store.SetJobTalkBinding(context.Background(), "talk-interrupted", binding); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	interruptedAt := nowUTCString()
	if _, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), interruptedAt); err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}

	// A moderator restarted recording in the same room before the notify
	// goroutine got to this job.
	live := &talkRoomState{
		RoomKey:    talkRoomKey(fakeTalk.server.URL, "room123"),
		BackendURL: fakeTalk.server.URL,
		RoomToken:  "room123",
		Owner:      "chima",
	}
	if !rt.reserveTalkRoom(live) {
		t.Fatalf("expected to reserve live room state")
	}
	rt.bindTalkRoomJob(live, "job-live")

	rt.NotifyInterruptedTalkRecordings(interruptedAt)
	fakeTalk.assertEventTypes(t, nil)
}

func TestRedeliverTalkRecordingSkipsStoppedCallbackWhenRoomIsLive(t *testing.T) {
	// Regression: redelivery of an old job's recording must not send a
	// room-scoped stopped callback while a NEW recording is live in the same
	// room — spreed would mark the live recording stopped while its recorder
	// keeps running (D-352). The upload itself still proceeds: the store
	// endpoint never touches room recording state.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	seedJobRow(t, rt.store.db, seededJobRow{ID: "talk-undelivered", Stage: "done", State: "succeeded", CreatedAt: "2026-06-12T10:00:00Z", CompletedAt: strPtr("2026-06-12T10:30:00Z")})
	binding := `{"backend_url":"` + fakeTalk.server.URL + `","room_token":"room123","owner":"chima"}`
	if err := rt.store.SetJobTalkBinding(context.Background(), "talk-undelivered", binding); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	runPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(runPath, "recording.mkv"), []byte("old-recording"), 0o644); err != nil {
		t.Fatalf("write fixture recording: %v", err)
	}

	// A newer recording is live in the same room when the rerun redelivers.
	live := &talkRoomState{
		RoomKey:    talkRoomKey(fakeTalk.server.URL, "room123"),
		BackendURL: fakeTalk.server.URL,
		RoomToken:  "room123",
		Owner:      "chima",
	}
	if !rt.reserveTalkRoom(live) {
		t.Fatalf("expected to reserve live room state")
	}
	rt.bindTalkRoomJob(live, "job-live")

	job := mustGetJob(t, rt.store, "talk-undelivered")
	job.ArtifactRunPath = &runPath
	rt.redeliverTalkRecording(job)

	fakeTalk.assertEventTypes(t, nil)
	fakeTalk.assertUploadCount(t, 1)
	redelivered := mustGetJob(t, rt.store, "talk-undelivered")
	if redelivered.TalkDeliveredAt == nil {
		t.Fatalf("expected talk_delivered_at after upload-only redelivery, got nil")
	}
}

type fakeTalkServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	events  []string
	uploads int
	files   []string
	// storeFailures injects HTTP 500 responses on the store (upload)
	// endpoint: n > 0 fails the next n requests, -1 fails every request.
	storeFailures int
	storeRequests int
	// stoppedDelay holds "stopped" callbacks open before responding, to
	// exercise the client-side timeout.
	stoppedDelay time.Duration
}

func newFakeTalkServer(t *testing.T) *fakeTalkServer {
	t.Helper()
	ft := &fakeTalkServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ocs/v2.php/apps/spreed/api/v1/recording/backend", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(talkRecordingRandomHeader); len(got) < 32 {
			t.Errorf("%s length = %d, want >= 32", talkRecordingRandomHeader, len(got))
		}
		if r.Header.Get(talkRecordingRandomHeader) == "" {
			t.Errorf("missing %s header", talkRecordingRandomHeader)
		}
		if r.Header.Get(talkRecordingChecksumHeader) == "" {
			t.Errorf("missing %s header", talkRecordingChecksumHeader)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode backend payload: %v", err)
		}
		eventType, _ := payload["type"].(string)
		ft.mu.Lock()
		ft.events = append(ft.events, eventType)
		stoppedDelay := ft.stoppedDelay
		ft.mu.Unlock()
		if eventType == "stopped" && stoppedDelay > 0 {
			time.Sleep(stoppedDelay)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`))
	})
	mux.HandleFunc("/ocs/v2.php/apps/spreed/api/v1/recording/room123/store", func(w http.ResponseWriter, r *http.Request) {
		ft.mu.Lock()
		ft.storeRequests++
		fail := ft.storeFailures != 0
		if ft.storeFailures > 0 {
			ft.storeFailures--
		}
		ft.mu.Unlock()
		if fail {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if got := r.Header.Get(talkRecordingRandomHeader); len(got) < 32 {
			t.Errorf("%s length = %d, want >= 32", talkRecordingRandomHeader, len(got))
		}
		if r.Header.Get(talkRecordingRandomHeader) == "" {
			t.Errorf("missing %s header", talkRecordingRandomHeader)
		}
		if r.Header.Get(talkRecordingChecksumHeader) == "" {
			t.Errorf("missing %s header", talkRecordingChecksumHeader)
		}
		if r.ContentLength <= 0 {
			t.Errorf("upload Content-Length = %d, want > 0", r.ContentLength)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		if got := r.FormValue("owner"); got != "chima" {
			t.Errorf("owner = %q, want chima", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.ReadAll(file)
		_ = file.Close()
		ft.mu.Lock()
		ft.uploads++
		ft.files = append(ft.files, header.Filename)
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

func (f *fakeTalkServer) setStoreFailures(n int) {
	f.mu.Lock()
	f.storeFailures = n
	f.mu.Unlock()
}

func (f *fakeTalkServer) setStoppedDelay(d time.Duration) {
	f.mu.Lock()
	f.stoppedDelay = d
	f.mu.Unlock()
}

func (f *fakeTalkServer) storeRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.storeRequests
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

func (f *fakeTalkServer) assertUploadFilenames(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range f.files {
		if !isTimestampedRecordingName(name) {
			t.Fatalf("upload filename = %q, want recording-YYYYMMDDTHHMMSS.nnnnnnnnnZ.mkv", name)
		}
	}
}

func isTimestampedRecordingName(name string) bool {
	if len(name) != len("recording-20060102T150405.000000000Z.mkv") {
		return false
	}
	if !strings.HasPrefix(name, "recording-") || !strings.HasSuffix(name, "Z.mkv") {
		return false
	}
	for i, ch := range name {
		switch i {
		case 18:
			if ch != 'T' {
				return false
			}
		case 25:
			if ch != '.' {
				return false
			}
		case 35:
			if ch != 'Z' {
				return false
			}
		case 36:
			if ch != '.' {
				return false
			}
		case 37, 38, 39:
			if name[i] != "mkv"[i-37] {
				return false
			}
		default:
			if i >= 10 && i <= 34 && (ch < '0' || ch > '9') {
				return false
			}
		}
	}
	return true
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
