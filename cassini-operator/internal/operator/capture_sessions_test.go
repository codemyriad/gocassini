package operator

import (
	"cassini-operator/internal/operator/appapi"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureSessionsSameOwnerAndMillisecondStaySeparate(t *testing.T) {
	rt := captureTestRuntime(t)
	first := validSidecar()
	first.CaptureID = "capture-a"
	first.Segments[0].SessionID = "session-a"
	second := first
	second.CaptureID = "capture-b"
	second.Segments = append([]captureSegment(nil), first.Segments...)
	second.Segments[0].SessionID = "session-b"
	post := func(sidecar captureSidecar, body string) int {
		req := uploadRequest(t, sidecar, map[string][]byte{"segment-0.webm": []byte(body)})
		req = req.WithContext(appapi.WithUserID(context.Background(), "alice"))
		rec := httptest.NewRecorder()
		rt.captureUploadHandler(nil, quietLogger())(rec, req)
		return rec.Code
	}
	if got := post(first, "first browser audio"); got != 202 {
		t.Fatalf("first upload: %d", got)
	}
	if got := post(second, "different second browser audio"); got != 202 {
		t.Fatalf("second upload: %d", got)
	}
	for _, item := range []struct {
		sidecar captureSidecar
		body    string
	}{{first, "first browser audio"}, {second, "different second browser audio"}} {
		raw, err := os.ReadFile(filepath.Join(captureSidecarDir(rt.cfg.CaptureRoot, "alice", &item.sidecar), "segment-0.webm"))
		if err != nil || string(raw) != item.body {
			t.Fatalf("upload overwritten: %q %v", raw, err)
		}
	}
	set, err := scanSourceCapturesForRecording(rt.cfg.CaptureRoot, first.RoomToken, captureRecordingWindow{StartMS: first.CallStartWallMS, EndMS: first.CallEndWallMS})
	if err != nil || set.Count != 2 || len(set.Uploads) != 2 {
		t.Fatalf("separate sessions refused: %+v %v", set, err)
	}
	for _, upload := range set.Uploads {
		if upload.ExclusionReason != "" || upload.CaptureID == "" || len(upload.SessionIDs) != 1 {
			t.Fatalf("session evidence: %+v", upload)
		}
	}
	// Retrying with a different session id must not relabel an existing file.
	first.Segments[0].SessionID = "session-other"
	if got := post(first, "first browser audio"); got != 409 {
		t.Fatalf("session identity changed on retry: %d", got)
	}
}

func TestCaptureExpectationsMatchCaptureIDNotJustUserAndStart(t *testing.T) {
	rt := &Runtime{store: newRebuildTestStore(t), ctx: context.Background()}
	rt.cfg.CaptureRoot = t.TempDir()
	now := time.Now().UTC()
	start := now.Add(-time.Minute)
	seedRecording(t, rt.store, "call", "room", formatUTCString(start), formatUTCString(now))
	setJobState(t, rt.store, "call", "build", "queued")
	for _, id := range []string{"a", "b"} {
		raw, _ := json.Marshal(map[string]any{"roomToken": "room", "callStartWallMs": start.UnixMilli(), "callEndWallMs": now.UnixMilli(), "captureId": id, "sessionId": "session-" + id, "status": "uploading"})
		req := httptest.NewRequest(http.MethodPost, "/capture/register", strings.NewReader(string(raw))).WithContext(appapi.WithUserID(rt.ctx, "alice"))
		rec := httptest.NewRecorder()
		rt.captureRegisterHandler(nil)(rec, req)
		if rec.Code != 200 {
			t.Fatalf("register %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	uploaded := []sourceAudioUpload{{Owner: "alice", CaptureID: "a", CallStartMS: start.UnixMilli(), CallEndMS: now.UnixMilli(), Complete: true}}
	expected, deadline, err := rt.captureExpectationsForJob(rt.ctx, "call", uploaded)
	if err != nil || len(expected) != 2 || deadline == nil {
		t.Fatalf("expectations: %+v %v %v", expected, deadline, err)
	}
	stored, waiting := 0, 0
	for _, entry := range expected {
		if entry.Status == "stored" {
			stored++
		} else {
			waiting++
		}
	}
	if stored != 1 || waiting != 1 {
		t.Fatalf("one upload satisfied both browsers: %+v", expected)
	}
}
