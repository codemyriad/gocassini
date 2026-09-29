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

	"cassini-operator/internal/operator/appapi"
)

func TestDirectShareProxyListsAndReadsAsCallerThenRevokes(t *testing.T) {
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	if err := metadata.Put(context.Background(), 11, "A.opus", json.RawMessage(`{"id":"A","title":"Planning","audioPath":"./meetings/A.opus"}`)); err != nil {
		t.Fatal(err)
	}
	shared := true
	var ownerMediaReads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/files_sharing/api/v1/shares"):
			if shared {
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[{"id":1,"share_type":0,"uid_file_owner":"cassini","file_source":11,"permissions":1,"path":"/Shared/Renamed.opus","item_type":"file"}]}}`)
			} else {
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
			}
		case strings.HasPrefix(r.URL.Path, "/remote.php/dav/files/cassini/"):
			ownerMediaReads++
			http.Error(w, "owner read is forbidden in this test", 500)
		case r.URL.Path == "/remote.php/dav/files/alice/Shared/Renamed.opus":
			if !shared {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, "recording bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret",
		PublishSink: publishSinkNextcloudFiles, meetingMetadata: metadata, sharePaths: &recordingSharePathCache{}}
	proxy := cfg.ncFilesProxy(nil, searchDeps{})
	request := func(rel string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/published/"+rel, nil)
		r = r.WithContext(appapi.WithUserID(r.Context(), "alice"))
		w := httptest.NewRecorder()
		if !proxy(w, r, rel) {
			t.Fatalf("proxy declined %s", rel)
		}
		return w
	}
	listed := request(meetingsListPath)
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"Planning"`) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	media := request("meetings/A.opus")
	if media.Code != 200 || media.Body.String() != "recording bytes" {
		t.Fatalf("media = %d %s", media.Code, media.Body.String())
	}
	shared = false
	// The path is still cached; Nextcloud's caller-authenticated DAV read must
	// nevertheless refuse the bytes immediately after the share is revoked.
	media = request("meetings/A.opus")
	if media.Code != 404 {
		t.Fatalf("revoked media = %d %s", media.Code, media.Body.String())
	}
	listed = request(meetingsListPath)
	if listed.Code != 200 || strings.Contains(listed.Body.String(), `"Planning"`) {
		t.Fatalf("revoked list = %d %s", listed.Code, listed.Body.String())
	}
	if ownerMediaReads != 0 {
		t.Fatalf("made %d owner media reads", ownerMediaReads)
	}
}
