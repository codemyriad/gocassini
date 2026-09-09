package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

func TestCaptureRecordingIdentityRequiresOneLiveRecording(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	handler := rt.captureRecordingHandler(nil)
	read := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/capture/recording?room=abc123", nil)
		req = req.WithContext(appapi.WithUserID(context.Background(), "alice"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := read(); rec.Code != 409 {
		t.Fatalf("no recording: %d", rec.Code)
	}
	seedRecording(t, rt.store, "live", "abc123", nowUTCString(), "")
	setJobState(t, rt.store, "live", "record", "running")
	if rec := read(); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("live")) {
		t.Fatalf("live identity: %d %s", rec.Code, rec.Body)
	}
	seedRecording(t, rt.store, "overlapping", "abc123", nowUTCString(), "")
	setJobState(t, rt.store, "overlapping", "record", "running")
	if rec := read(); rec.Code != 409 {
		t.Fatalf("ambiguous recording: %d", rec.Code)
	}
	handler = rt.captureRecordingHandler(func(context.Context, string, string) (bool, error) { return false, nil })
	if rec := read(); rec.Code != 403 {
		t.Fatalf("nonmember: %d", rec.Code)
	}
}

func TestCaptureTransferResumeCommitAndOwnership(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	sidecar := validSidecar()
	sidecar.RecordingID, sidecar.SessionID = "job-transfer", "session-a"
	window := captureRecordingWindow{StartMS: sidecar.CallStartWallMS, EndMS: sidecar.CallEndWallMS}
	seedFinishedRecording(t, rt, sidecar.RecordingID, sidecar.RoomToken, window)
	endpoint := "/capture/transfer/abc123/job-transfer/session-a"
	handler := rt.captureTransferHandler(nil, rt.logger)
	request := func(method, url, owner, contentType string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, body)
		req.Header.Set("Content-Type", contentType)
		req = req.WithContext(appapi.WithUserID(context.Background(), owner))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	data := []byte("immutable audio")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	piece := func(payload []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("piece", hash+".part")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return request("POST", endpoint+"/"+hash, "alice", writer.FormDataContentType(), &body)
	}
	if rec := piece([]byte("corruption")); rec.Code != 422 {
		t.Fatalf("hash refusal: %d %s", rec.Code, rec.Body)
	}
	// The rename succeeded but fsync failed. Neither an inventory nor an
	// identical POST may turn that into an acknowledgement while sync fails.
	originalSync := syncCaptureDir
	t.Cleanup(func() { syncCaptureDir = originalSync })
	syncCaptureDir = func(string) error { return errors.New("injected fsync failure") }
	if rec := piece(data); rec.Code != 503 {
		t.Fatalf("sync failure: %d", rec.Code)
	}
	if rec := piece(data); rec.Code != 503 {
		t.Fatalf("retry acknowledged unsynced piece: %d", rec.Code)
	}
	if rec := request("GET", endpoint, "alice", "", nil); rec.Code != 503 {
		t.Fatalf("inventory acknowledged unsynced piece: %d", rec.Code)
	}
	syncCaptureDir = originalSync
	for i := 0; i < 2; i++ {
		if rec := piece(data); rec.Code != 204 {
			t.Fatalf("piece/replay: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := request("GET", endpoint, "bob", "", nil); bytes.Contains(rec.Body.Bytes(), []byte(hash)) {
		t.Fatal("another account can see Alice's inventory")
	}
	if rec := request("GET", "/capture/transfer/other/job-transfer/session-a", "alice", "", nil); rec.Code != 403 {
		t.Fatalf("room binding: %d", rec.Code)
	}
	sidecar.ClockSamples = []captureClockSample{clockSample(sidecar.CallStartWallMS-5000, 5000, 10, 0), clockSample(sidecar.CallEndWallMS-5000, 5000, 10, 0)}
	// Caller-supplied server fields must not select placement or break retry.
	sidecar.ClockStatus, sidecar.ClockCorrectionMS, sidecar.ClockVariationMS = "corrected", 999999, 999999
	manifest := captureTransferManifest{Sidecar: sidecar, Pieces: map[string][]string{"segment-0.webm": {hash}}}
	commit := func() *httptest.ResponseRecorder {
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		return request("POST", endpoint+"/commit", "alice", "application/json", bytes.NewReader(raw))
	}
	for i := 0; i < 2; i++ {
		manifest.Sidecar.ClockVariationMS++ // server-only diagnostics cannot change request identity
		if rec := commit(); rec.Code != 202 {
			t.Fatalf("commit/replay: %d %s", rec.Code, rec.Body)
		}
	}
	dir := transferDir(rt.cfg.CaptureRoot, "abc123", "alice", "job-transfer", "session-a")
	rawStored, err := os.ReadFile(filepath.Join(dir, captureSidecarName))
	if err != nil {
		t.Fatal(err)
	}
	var stored captureSidecar
	if err := json.Unmarshal(rawStored, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ClockStatus != "corrected" || stored.ClockCorrectionMS != 5000 || stored.ClockVariationMS != 0 || stored.CallStartWallMS != sidecar.CallStartWallMS-5000 {
		t.Fatalf("intake correction: %+v", stored)
	}
	got, err := os.ReadFile(filepath.Join(dir, "segment-0.webm"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("assembled bytes: %q %v", got, err)
	}
	seq, err := rt.store.SourceAudioUploadSeq(context.Background(), "job-transfer")
	if err != nil || seq != 1 {
		t.Fatalf("replay scheduled another generation: %d %v", seq, err)
	}
	manifest.Sidecar.Segments[0].StartWallMS++
	if rec := commit(); rec.Code != 409 {
		t.Fatalf("changed manifest should conflict: %d %s", rec.Code, rec.Body)
	}
	// Simulate a process dying after manifest promotion but before the receipt
	// transaction. Recovery discovers the commit and atomically schedules once.
	if _, err := rt.store.db.Exec("DELETE FROM capture_receipts"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec("UPDATE jobs SET source_audio_upload_seq=0 WHERE id='job-transfer'"); err != nil {
		t.Fatal(err)
	}
	rt.reconcileCaptureReceipts()
	rt.reconcileCaptureReceipts()
	seq, err = rt.store.SourceAudioUploadSeq(context.Background(), "job-transfer")
	if err != nil || seq != 1 {
		t.Fatalf("recovery not idempotent: %d %v", seq, err)
	}
	// The server identity selects this session even when another recording has
	// the same wall window, without admitting it to that other recording.
	set, err := scanSourceCapturesForRecording(rt.cfg.CaptureRoot, "abc123", window, "another-job")
	if err != nil || set.Count != 0 {
		t.Fatalf("cross-recording selection: %+v %v", set, err)
	}
	set, err = scanSourceCapturesForRecording(rt.cfg.CaptureRoot, "abc123", window, "job-transfer")
	if err != nil || set.Count != 1 {
		t.Fatalf("exact recording selection: %+v %v", set, err)
	}
}

func TestCaptureDigestIncludesBytesAndPlacement(t *testing.T) {
	root := t.TempDir()
	start := ms(t, "2026-09-02T10:00:00Z")
	dir := seedRebuildCapture(t, root, "room-a", "alice", start, start+60000, 64)
	window := captureRecordingWindow{StartMS: start, EndMS: start + 60000}
	scan := func() string {
		set, err := scanSourceCapturesForRecording(root, "room-a", window)
		if err != nil {
			t.Fatal(err)
		}
		return set.Digest
	}
	before := scan()
	data := make([]byte, 64)
	data[0] = 1
	if err := os.WriteFile(filepath.Join(dir, "segment-0.webm"), data, 0600); err != nil {
		t.Fatal(err)
	}
	changed := scan()
	if changed == before {
		t.Fatal("equal-size changed audio has the same digest")
	}
	raw, err := os.ReadFile(filepath.Join(dir, captureSidecarName))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar captureSidecar
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatal(err)
	}
	sidecar.Segments[0].StartWallMS++
	raw, err = json.Marshal(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, captureSidecarName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if scan() == changed {
		t.Fatal("changed placement has the same digest")
	}
}

func TestCaptureReceiptTransactionRollsBackForMissingJob(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	if err := rt.store.noteCaptureReceipt(context.Background(), "missing", "receipt", nowUTCString()); err == nil {
		t.Fatal("missing job accepted")
	}
	known, err := rt.store.captureReceiptKnown(context.Background(), "receipt")
	if err != nil || known {
		t.Fatalf("failed transaction retained receipt: %v %v", known, err)
	}
}

func TestCaptureRecordingClockAfterStop(t *testing.T) {
	rt, cleanup := rebuildRuntime(t)
	defer cleanup()
	req := httptest.NewRequest("GET", "/capture/recording?room=abc123", nil)
	req = req.WithContext(appapi.WithUserID(context.Background(), "alice"))
	rec := httptest.NewRecorder()
	before := time.Now().UnixMilli()
	rt.captureRecordingHandler(nil).ServeHTTP(rec, req)
	var body struct {
		Receive int64 `json:"serverReceiveWallMs"`
		Send    int64 `json:"serverSendWallMs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 409 || body.Receive < before || body.Send < body.Receive || body.Send > time.Now().UnixMilli() {
		t.Fatalf("missing bounded stop sample: %d %+v", rec.Code, body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("clock response may be cached")
	}
}
