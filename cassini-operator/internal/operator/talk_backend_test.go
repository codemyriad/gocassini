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
	// Status only: no upload, no other Talk route (D-551).
	fakeTalk.assertNoOtherTalkRoutes(t)
	job, err := rt.store.GetJob(req.Context(), jobs[0].ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.TalkDeliveredAt == nil {
		t.Fatalf("expected talk_delivered_at set after an acknowledged stopped callback, got nil")
	}
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
	fakeTalk.assertNoOtherTalkRoutes(t)
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
	fakeTalk.assertNoOtherTalkRoutes(t)
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
	fakeTalk.assertNoOtherTalkRoutes(t)
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

func TestTalkRerunSendsNothingToTalk(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.talkRetryDelays = []time.Duration{time.Millisecond}
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
	job := waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	if job.TalkBinding == nil || !strings.Contains(*job.TalkBinding, `"room_token":"room123"`) {
		t.Fatalf("expected persisted talk binding, got %#v", job.TalkBinding)
	}

	// A rerun re-runs build/publish only. It must not replay the room-scoped
	// stopped callback and must not upload anything to Talk (D-551): the
	// meeting reaches Nextcloud solely as the published .opus.
	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+job.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, job.ID, "succeeded")

	// Exactly the two status callbacks from the original recording — the rerun
	// added none — and no other Talk route was touched at all.
	fakeTalk.assertEventTypes(t, []string{"started", "stopped"})
	fakeTalk.assertNoOtherTalkRoutes(t)
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
	// must give up and let the pipeline finish instead of stalling the
	// post-record path. The stopped marker stays unset, so the startup sweep
	// is still free to tell spreed the recording failed.
	job := waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
	if job.TalkDeliveredAt != nil {
		t.Fatalf("expected the stopped marker to stay unset after callback timeouts, got %#v", job.TalkDeliveredAt)
	}
	fakeTalk.assertNoOtherTalkRoutes(t)
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

type fakeTalkServer struct {
	server *httptest.Server
	mu     sync.Mutex
	events []string
	// stoppedDelay holds "stopped" callbacks open before responding, to
	// exercise the client-side timeout.
	stoppedDelay time.Duration
	// unexpected records any request to a route other than the recording
	// backend callback. Cassini must talk to spreed for status only (D-551),
	// so anything else — above all the recording store endpoint — is a bug.
	unexpected []string
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
	// Catch-all: the operator must never call any other Talk route. This is
	// what proves the recording store endpoint is gone (D-551) — it fails the
	// test loudly rather than letting a reintroduced upload pass unnoticed.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		ft.mu.Lock()
		ft.unexpected = append(ft.unexpected, r.Method+" "+r.URL.Path)
		ft.mu.Unlock()
		t.Errorf("unexpected Talk request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	ft.server = httptest.NewServer(mux)
	return ft
}

func (f *fakeTalkServer) Close() {
	f.server.Close()
}

func (f *fakeTalkServer) setStoppedDelay(d time.Duration) {
	f.mu.Lock()
	f.stoppedDelay = d
	f.mu.Unlock()
}

// assertNoOtherTalkRoutes fails when the operator hit any Talk route other than
// the recording-backend status callback — the recording store above all.
func (f *fakeTalkServer) assertNoOtherTalkRoutes(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.unexpected) != 0 {
		t.Fatalf("operator called non-callback Talk routes: %#v", f.unexpected)
	}
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

type assertErr string

func (e assertErr) Error() string { return string(e) }
