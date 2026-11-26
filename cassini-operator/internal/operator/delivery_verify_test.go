package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// After the upload, the delivery check reads the recording back from
// Nextcloud and hashes it as it arrives: nothing lands on the work volume
// beside the file being delivered, and a read-back that differs is refused.
func TestDeliveryCheckHashesTheReadBackWithoutWritingIt(t *testing.T) {
	_, s, _, _ := asyncFixture(t)
	ctx := context.Background()
	data, _ := json.Marshal(annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000})
	dir := t.TempDir()
	local := filepath.Join(dir, "sealed.opus")
	if err := os.WriteFile(local, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var gets atomic.Int32
	var corrupt atomic.Bool
	transport := s.client.Transport
	s.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Errorf("work dir during the read-back: %v %v", entries, err)
			}
		}
		resp, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodGet && corrupt.Load() {
			drainClose(resp.Body)
			resp.Body = io.NopCloser(strings.NewReader("not what was uploaded"))
		}
		return resp, err
	})
	sink := &nextcloudFilesPublishSink{cfg: s.exapp, client: s.client, cassiniBin: s.bin, rt: s.rt}
	item := upload{local: local, remote: annTestRecording, size: int64(len(data))}

	if err := sink.putAssetBytes(ctx, item, ""); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if gets.Load() == 0 {
		t.Fatal("the delivery was never read back")
	}
	corrupt.Store(true)
	if err := sink.putAssetBytes(ctx, item, ""); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("a read-back that differs: %v", err)
	}
}

// The read-back is bounded by the size just uploaded, not by the limit on
// recordings staged for annotation: an import can be up to 4 GiB.
func TestDeliveryReadBackIsBoundedByTheUploadedSize(t *testing.T) {
	big := upload{size: maxImportUploadBytes}
	if got := readBackLimit(big); got != maxImportUploadBytes {
		t.Fatalf("a %d-byte upload is read back up to %d bytes", big.size, got)
	}
	if maxImportUploadBytes <= maxAnnotateRecordingBytes {
		t.Fatal("this test no longer covers an upload above the recording limit")
	}
	if got := readBackLimit(upload{}); got != maxAnnotateRecordingBytes {
		t.Fatalf("an upload of unknown size is read back up to %d bytes", got)
	}
}

func TestDAVHashFileHashesWithoutWriting(t *testing.T) {
	body := "meeting bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing.opus") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := ExAppConfig{NextcloudURL: srv.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	ctx := context.Background()

	digest, written, err := c.davHashFile(ctx, srv.Client(), "alice", "m.opus", 0)
	want := sha256.Sum256([]byte(body))
	if err != nil || written != int64(len(body)) || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("hash: %q %d %v", digest, written, err)
	}
	if _, _, err := c.davHashFile(ctx, srv.Client(), "alice", "m.opus", 4); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("over the limit: %v", err)
	}
	if _, _, err := c.davHashFile(ctx, srv.Client(), "alice", "missing.opus", 0); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing: %v", err)
	}
}

// An import re-run after its upload landed, but before the upload was
// verified and its marks recorded, finds Nextcloud already holding the file.
// It still checks Nextcloud's copy and records the delivery.
func TestAnUploadThatLandedIsStillVerifiedAndRecorded(t *testing.T) {
	_, s, _, store := asyncFixture(t)
	ctx := context.Background()
	data, _ := json.Marshal(annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000})
	local := filepath.Join(t.TempDir(), "sealed.opus")
	if err := os.WriteFile(local, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var corrupt atomic.Bool
	transport := s.client.Transport
	s.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		resp, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodGet && corrupt.Load() {
			drainClose(resp.Body)
			resp.Body = io.NopCloser(strings.NewReader("not what was uploaded"))
		}
		return resp, err
	})
	sink := &nextcloudFilesPublishSink{cfg: s.exapp, client: s.client, cassiniBin: s.bin, rt: s.rt}
	item := upload{local: local, remote: annTestRecording, size: int64(len(data))}
	pending := func() bool {
		t.Helper()
		var n int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM annotation_head WHERE republish_json IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}

	// The first attempt uploads, then stops at the check.
	corrupt.Store(true)
	if err := sink.putAssetBytes(ctx, item, ""); err == nil {
		t.Fatal("a read-back that differs was accepted")
	}
	if !pending() {
		t.Fatal("the stopped attempt left no delivery to finish")
	}
	// A re-run that finds the file still refuses a copy that differs...
	if err := sink.confirmAssetDelivery(ctx, item); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("re-run with a read-back that differs: %v", err)
	}
	// ...and records the delivery once Nextcloud's copy checks out.
	corrupt.Store(false)
	if err := sink.confirmAssetDelivery(ctx, item); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if pending() {
		t.Fatal("the re-run left the delivery unrecorded")
	}
}

// A cancel or shutdown that arrives while a delivered import is being
// indexed does not stop the indexing: the import is recorded as done, and a
// done meeting must be searchable.
func TestIndexingADeliveredImportOutlivesACancel(t *testing.T) {
	bin := fakeCassini(t, `printf '%s' '{"segments":[{"speaker":"S1","words":[{"text":"budget","startMs":0,"endMs":400}]}]}'`)
	imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: bin, rt: &Runtime{searchStore: newTestSearchStore(t)}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	item := upload{local: "MEETING1.opus", remote: annTestRecording}
	if !imp.indexDelivered(ctx, item, "MEETING1.opus", "MEETING1.opus", "digest") {
		t.Fatal("a canceled import's delivered meeting was left out of search")
	}
}

// #510: a file the importer refuses reads as an invalid file, so the sheet
// can say so in the reader's language; the CLI's words stay as the error. A
// CLI run that could not do its work is not the file's fault, and has no code.
func TestAnInvalidFileFailsWithItsCode(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		name, body, cli string
		want            string
	}{
		{"neither Ogg nor JSON", "RIFF....WAVE", `exit 0`, importFailInvalidFile},
		{"refused by the CLI", "OggS-not-a-meeting", `echo "portable opus file is missing CASSINI_FORMAT" >&2; exit 1`, importFailInvalidFile},
		{"skipped by the CLI", "OggS-not-a-meeting", `echo "skipped x (not a portable meeting)"; exit 0`, importFailInvalidFile},
		{"disk full", "OggS-a-meeting", `echo "publish failed: write site/x: no space left on device" >&2; exit 1`, ""},
		{"killed", "OggS-a-meeting", `kill -9 $$`, ""},
		{"no CLI", "OggS-a-meeting", "", ""},
	} {
		bin := filepath.Join(t.TempDir(), "missing-cassini")
		if tc.cli != "" {
			bin = fakeCassini(t, tc.cli)
		}
		imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: bin, rt: &Runtime{store: store}}}
		local := filepath.Join(t.TempDir(), "staged")
		if err := os.WriteFile(local, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := imp.run(context.Background(), meetingImportRequest{ImportID: "01INVALID", CreatedBy: "alice", LocalPath: local}, nil)
		if err == nil || importFailureCode(err.Error()) != tc.want {
			t.Fatalf("%s: %v (code %q, want %q)", tc.name, err, importFailureCode(err.Error()), tc.want)
		}
	}
}
