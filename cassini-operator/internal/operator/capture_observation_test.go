package operator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

func TestSourceAudioObservationSeparatesStoredAudioFromUse(t *testing.T) {
	root := t.TempDir()
	start := ms(t, "2026-09-02T10:00:00Z")
	end := start + 60_000
	dir := seedRebuildCapture(t, root, "room", "alice", start, end, 123)
	seedRebuildCapture(t, root, "other-room", "bob", start, end, 456)
	set, err := scanSourceCapturesForRecording(root, "room", captureRecordingWindow{StartMS: start, EndMS: end})
	if err != nil || len(set.Uploads) != 1 {
		t.Fatalf("scan: %+v %v", set, err)
	}
	upload := set.Uploads[0]
	if upload.Owner != "alice" || upload.Bytes != 123 || upload.Segments != 1 || !upload.Complete || upload.ReceivedAt == nil {
		t.Fatalf("unexpected stored evidence: %+v", upload)
	}
	if err := os.Remove(filepath.Join(dir, "segment-0.webm")); err != nil {
		t.Fatal(err)
	}
	set, err = scanSourceCapturesForRecording(root, "room", captureRecordingWindow{StartMS: start, EndMS: end})
	if err != nil || set.Uploads[0].Complete || set.Uploads[0].Bytes != 0 {
		t.Fatalf("missing audio shown as complete: %+v %v", set, err)
	}
	// Upload acceptance alone supplies no build evidence.
	if evidence := readSourceAudioAttempt(1, t.TempDir()); evidence.Available || len(evidence.Participants) != 0 {
		t.Fatalf("missing manifest became proof of use: %+v", evidence)
	}
}

func TestSourceAudioObservationUsesEachAttemptsOwnManifest(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(one, "manifest.json"), []byte(`{"provenance":{"sourceAudio":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(two, "manifest.json"), []byte(`{"provenance":{"sourceAudio":[{"owner":"alice","segments":2,"placed":1,"skipped":1,"spliced_ms":8000,"mix_spliced":false,"transcript_source":"per-participant","rejections":["segment 1 not spliced: incomplete audio"]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	first, second := readSourceAudioAttempt(1, one), readSourceAudioAttempt(2, two)
	if !first.Available || len(first.Participants) != 0 || !second.Available || len(second.Participants) != 1 {
		t.Fatalf("attempts: %+v %+v", first, second)
	}
	use := second.Participants[0]
	if use.MixSpliced || use.TranscriptSource != "per-participant" || use.Skipped != 1 || len(use.Rejections) != 1 {
		t.Fatalf("lost partial-use evidence: %+v", use)
	}
	if err := os.WriteFile(filepath.Join(two, "manifest.json"), []byte(`{"provenance":`), 0600); err != nil {
		t.Fatal(err)
	}
	bad := readSourceAudioAttempt(2, two)
	if bad.Available || bad.Error == "" {
		t.Fatalf("invalid manifest looks successful: %+v", bad)
	}
}

func TestSourceAudioObservationTracksReceivingUntilUploadReturns(t *testing.T) {
	rt := captureTestRuntime(t)
	original := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": make([]byte, 128<<10)})
	body, err := io.ReadAll(original.Body)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	original.Body = reader
	original = original.WithContext(appapi.WithUserID(context.Background(), "authenticated-owner"))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); rt.captureUploadHandler(nil, quietLogger())(response, original) }()
	if _, err := writer.Write(body[:len(body)/2]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var receiving *captureTransfer
	for time.Now().Before(deadline) {
		rt.captureTransfers.Range(func(key, _ any) bool { receiving = key.(*captureTransfer); return false })
		if receiving != nil && receiving.bytes.Load() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if receiving == nil || receiving.owner != "authenticated-owner" || receiving.bytes.Load() <= 0 {
		t.Fatalf("no authenticated receiving evidence: %+v", receiving)
	}
	if _, err := writer.Write(body[len(body)/2:]); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not finish")
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	rt.captureTransfers.Range(func(_, _ any) bool { t.Error("completed upload still marked receiving"); return false })
	var accepted struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if receiving.bytes.Load() != accepted.Bytes {
		t.Fatalf("receiving bytes %d != accepted %d", receiving.bytes.Load(), accepted.Bytes)
	}
}

func TestSourceAudioEvidenceSurvivesRetentionAndDoesNotBorrowAnotherAttempt(t *testing.T) {
	root, meeting, canonical := t.TempDir(), t.TempDir(), t.TempDir()
	manifest := []byte(`{"provenance":{"sourceAudio":[{"owner":"alice","spliced_ms":8000,"mix_spliced":true,"transcript_source":"merged-mix"}]}}`)
	if err := os.WriteFile(filepath.Join(meeting, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveSourceAudioEvidence(root, "job", 1, meeting); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(meeting); err != nil {
		t.Fatal(err)
	}
	evidence := sourceAudioEvidence(root, "job", 1, meeting, "")
	if !evidence.Available || len(evidence.Participants) != 1 {
		t.Fatalf("retention erased evidence: %+v", evidence)
	}
	if err := os.WriteFile(filepath.Join(canonical, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "cassini.json"), []byte(`{"kind":"meeting","job_id":"job","attempt_number":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := sourceAudioEvidence(root, "job", 2, "", canonical); got.Available {
		t.Fatalf("attempt 2 borrowed attempt 3: %+v", got)
	}
	if got := sourceAudioEvidence(root, "other-job", 3, "", canonical); got.Available {
		t.Fatalf("borrowed another job: %+v", got)
	}
	if got := sourceAudioEvidence(root, "job", 3, "", canonical); !got.Available {
		t.Fatalf("matching legacy canonical evidence unavailable: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(attemptLogsDir(root, "job", 1), "source-audio.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := sourceAudioEvidence(root, "job", 1, "", canonical); got.Available || got.Error == "" {
		t.Fatalf("corrupt saved evidence: %+v", got)
	}
}
