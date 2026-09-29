package operator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectShareSnapshotIntersectsMetadataWithCurrentShares(t *testing.T) {
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	for id, name := range map[int64]string{11: "A.opus", 22: "SECRET.opus", 33: "ZERO.opus"} {
		entry := json.RawMessage(`{"id":"` + name[:1] + `","title":"` + name + `","audioPath":"./meetings/` + name + `"}`)
		if err := metadata.Put(context.Background(), id, name, entry); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[`+
			`{"uid_file_owner":"cassini","file_source":11,"permissions":1,"path":"/Shared/Renamed.opus","item_type":"file"},`+
			`{"uid_file_owner":"bob","file_source":22,"path":"/Shared/SECRET.opus","item_type":"file"},`+
			`{"uid_file_owner":"cassini","file_source":33,"permissions":0,"path":"/Shared/ZERO.opus","item_type":"file"}`+
			`]}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", sharePaths: &recordingSharePathCache{}}
	snapshot, err := cfg.directShareSnapshot(context.Background(), server.Client(), "alice", metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.entries) != 1 || snapshot.paths["A.opus"] != "Shared/Renamed.opus" || snapshot.paths["SECRET.opus"] != "" || snapshot.paths["ZERO.opus"] != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if p, ok := cfg.sharePaths.get("alice", "A.opus"); !ok || p != "Shared/Renamed.opus" {
		t.Fatalf("cache = %q, %v", p, ok)
	}
}

func TestCurrentRecordingPathBypassesStaleMediaCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
	}))
	defer server.Close()
	cache := &recordingSharePathCache{}
	cache.put("alice", map[string]string{"A.opus": "Shared/A.opus"})
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", sharePaths: cache}
	if _, err := cfg.currentRecordingPath(context.Background(), server.Client(), "alice", "A.opus", nil); err != errRecordingNotShared {
		t.Fatalf("current path after revocation = %v", err)
	}
}

func TestDirectShareSnapshotRecoversOriginalNameAfterIndexLoss(t *testing.T) {
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	var inventoryCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files_sharing/api/v1/shares") {
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[{"id":"9","uid_file_owner":"cassini","file_source":11,"permissions":1,"path":"/Shared/Renamed.opus","item_type":"file"}]}}`)
			return
		}
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" {
			t.Errorf("unexpected inventory request: %s %s", r.Method, r.URL.Path)
		}
		inventoryCalls++
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>/remote.php/dav/files/cassini/CassiniRecordings/meetings/A.opus</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>11</oc:fileid><d:resourcetype/></d:prop></d:propstat></d:response></d:multistatus>`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", sharePaths: &recordingSharePathCache{}}
	for i := 0; i < 2; i++ {
		snapshot, err := cfg.directShareSnapshot(context.Background(), server.Client(), "alice", metadata)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.entries) != 1 || snapshot.paths["A.opus"] != "Shared/Renamed.opus" || !strings.Contains(string(snapshot.entries[0]), `"audioPath":"./meetings/A.opus"`) {
			t.Fatalf("recovered snapshot = %+v", snapshot)
		}
	}
	if inventoryCalls != 1 {
		t.Fatalf("owner inventory calls = %d, want one cold recovery call", inventoryCalls)
	}
}
