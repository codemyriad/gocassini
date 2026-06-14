package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// seedTalkJob inserts a minimal job row and (when backendURL != "") a Talk
// binding owned by "chima" in room123 — matching what fakeTalkServer expects.
func seedTalkJob(t *testing.T, rt *Runtime, jobID, provider, backendURL string) {
	t.Helper()
	ctx := context.Background()
	now := nowUTCString()
	if _, err := rt.store.db.ExecContext(ctx, `
INSERT INTO jobs (id, provider, request_json, stage, state, current_attempt_number, rerun_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, provider, "{}", "build", "succeeded", 1, 0, now, now); err != nil {
		t.Fatalf("insert job %s: %v", jobID, err)
	}
	if backendURL == "" {
		return
	}
	binding := `{"backend_url":"` + backendURL + `","room_token":"room123","owner":"chima"}`
	if err := rt.store.SetJobTalkBinding(ctx, jobID, binding); err != nil {
		t.Fatalf("SetJobTalkBinding(%s): %v", jobID, err)
	}
}

func countSuffix(files []string, suffix string) int {
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, suffix) {
			n++
		}
	}
	return n
}

func TestDeliverTalkMeetingArtifactsUploadsRichBundle(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	jobID := "rich-artifacts-job"
	seedTalkJob(t, rt, jobID, "talk", fakeTalk.server.URL)

	meetingDir := t.TempDir()
	writeSiteFile(t, filepath.Join(meetingDir, "meeting.webm"), "WEBM-AUDIO-BYTES")
	writeSiteFile(t, filepath.Join(meetingDir, "summary.md"), "# Meeting summary\n\n- a point\n")

	rt.deliverTalkMeetingArtifacts(jobID, meetingDir)

	// Clean audio + readable summary are both delivered to the owner's files.
	fakeTalk.assertUploadCount(t, 2)
	files := fakeTalk.uploadedFiles()
	if got := countSuffix(files, ".webm"); got != 1 {
		t.Fatalf("uploaded .webm count = %d (files=%v), want 1", got, files)
	}
	if got := countSuffix(files, ".md"); got != 1 {
		t.Fatalf("uploaded .md count = %d (files=%v), want 1", got, files)
	}

	// Idempotent: a second delivery (e.g. a rerun) uploads nothing more.
	rt.deliverTalkMeetingArtifacts(jobID, meetingDir)
	fakeTalk.assertUploadCount(t, 2)

	if delivered, err := rt.store.TalkArtifactsDelivered(context.Background(), jobID); err != nil || !delivered {
		t.Fatalf("TalkArtifactsDelivered = %v, %v; want true, nil", delivered, err)
	}
}

func TestDeliverTalkMeetingArtifactsAudioOnlyWhenNoSummary(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	jobID := "audio-only-job"
	seedTalkJob(t, rt, jobID, "talk", fakeTalk.server.URL)

	// No LLM was configured, so only the clean audio exists (the CI shape).
	meetingDir := t.TempDir()
	writeSiteFile(t, filepath.Join(meetingDir, "meeting.webm"), "WEBM-AUDIO-BYTES")

	rt.deliverTalkMeetingArtifacts(jobID, meetingDir)

	fakeTalk.assertUploadCount(t, 1)
	if got := countSuffix(fakeTalk.uploadedFiles(), ".webm"); got != 1 {
		t.Fatalf("uploaded .webm count = %d, want 1", got)
	}
	if delivered, err := rt.store.TalkArtifactsDelivered(context.Background(), jobID); err != nil || !delivered {
		t.Fatalf("TalkArtifactsDelivered = %v, %v; want true, nil", delivered, err)
	}
}

func TestDeliverTalkMeetingArtifactsSkipsNonTalkJob(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	jobID := "manual-job"
	seedTalkJob(t, rt, jobID, "manual", "") // no Talk binding

	meetingDir := t.TempDir()
	writeSiteFile(t, filepath.Join(meetingDir, "meeting.webm"), "WEBM-AUDIO-BYTES")

	rt.deliverTalkMeetingArtifacts(jobID, meetingDir)
	fakeTalk.assertUploadCount(t, 0)
}

func TestDeliverTalkMeetingArtifactsIncompleteWhenAudioMissing(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	fakeTalk := newFakeTalkServer(t)
	defer fakeTalk.Close()

	jobID := "missing-audio-job"
	seedTalkJob(t, rt, jobID, "talk", fakeTalk.server.URL)

	// Only the optional summary is present; the required clean audio is not.
	meetingDir := t.TempDir()
	writeSiteFile(t, filepath.Join(meetingDir, "summary.md"), "# Summary\n")

	rt.deliverTalkMeetingArtifacts(jobID, meetingDir)

	// The summary still uploads, but delivery is left incomplete so a rerun
	// re-attempts it once the audio exists.
	if got := countSuffix(fakeTalk.uploadedFiles(), ".md"); got != 1 {
		t.Fatalf("uploaded .md count = %d, want 1", got)
	}
	if delivered, err := rt.store.TalkArtifactsDelivered(context.Background(), jobID); err != nil || delivered {
		t.Fatalf("TalkArtifactsDelivered = %v, %v; want false, nil (incomplete)", delivered, err)
	}
}

// TestBuildSuccessDeliversCleanAudioToOwnerFiles drives the whole Talk pipeline
// (start -> record -> build -> publish) with the delivery gate on and asserts
// the build stage delivers the clean audio to the owner's files alongside the
// raw recording. This is the in-repo stand-in for the live-Talk roundtrip e2e.
func TestBuildSuccessDeliversCleanAudioToOwnerFiles(t *testing.T) {
	rt, cleanup, _, startedPath := newCLITestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "secret-123"
	rt.cfg.DeliverMeetingArtifacts = true // production default; the test runtime opts in
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
		t.Fatalf("start status = %d, want 200 body=%s", startRec.Code, startRec.Body.String())
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
		t.Fatalf("stop status = %d, want 202 body=%s", stopRec.Code, stopRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, state.JobID, "succeeded")

	// The raw recording AND the clean audio both reached the owner's files.
	fakeTalk.assertUploadCount(t, 2)
	files := fakeTalk.uploadedFiles()
	if got := countSuffix(files, ".mkv"); got != 1 {
		t.Fatalf("uploaded .mkv count = %d (files=%v), want 1", got, files)
	}
	if got := countSuffix(files, ".webm"); got != 1 {
		t.Fatalf("uploaded .webm count = %d (files=%v), want 1", got, files)
	}
	if delivered, err := rt.store.TalkArtifactsDelivered(context.Background(), state.JobID); err != nil || !delivered {
		t.Fatalf("TalkArtifactsDelivered = %v, %v; want true, nil", delivered, err)
	}
}
