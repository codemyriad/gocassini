package operator

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectSharePublishStoresPrivateFileAndVerifiedShares(t *testing.T) {
	ctx := context.Background()
	rt, cleanup := newBarePublishRuntime(t, log.New(ioDiscard{}, "", 0))
	defer cleanup()
	const jobID = "DIRECT1"
	insertJob(t, rt.store.db, jobID, "2026-09-23T10:00:00Z")
	if err := rt.store.SetJobTalkBinding(ctx, jobID, `{"backend_url":"https://talk.example","room_token":"room1","owner":"alice","room_public":true}`); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.MergeJobRoomAudience(ctx, jobID, []aclMapping{{Type: "user", ID: "bob"}}, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	rt.meetingMetadata = metadata

	attempt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(attempt, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt, "meetings", jobID+".opus"), []byte("sealed bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt, "catalog.json"), []byte(`{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"DIRECT1","title":"Planning","audioPath":"./meetings/DIRECT1.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	files := newFakeNCFiles()
	filesServer := files.server(t)
	defer filesServer.Close()
	target, _ := url.Parse(filesServer.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	shares := map[string]int{}
	var created []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/files_sharing/api/v1/shares") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost {
			principal := r.FormValue("shareWith")
			shares[principal] = 17
			created = append(created, principal)
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":{"id":"11","share_type":0,"permissions":17}}}`)
			return
		}
		items := make([]map[string]any, 0, len(shares))
		for principal, permissions := range shares {
			items = append(items, map[string]any{"id": "11", "share_type": 0, "share_with": principal, "permissions": permissions})
		}
		body, _ := json.Marshal(map[string]any{"ocs": map[string]any{"meta": map[string]any{"statuscode": 100}, "data": items}})
		_, _ = w.Write(body)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", PublishSink: publishSinkNextcloudFiles}
	sink := &directSharesPublishSink{&nextcloudFilesPublishSink{cfg: cfg, client: server.Client(), logger: log.New(ioDiscard{}, "", 0), rt: rt}}
	ncAccessSubstrate.setProbe(ncStorageProbe{})
	if _, err := sink.Deliver(ctx, publishDelivery{AttemptSitePath: attempt, JobID: jobID, RoomName: "Room One"}); err != nil {
		t.Fatal(err)
	}
	remote := ncDefaultRecordingsRoot + "/meetings/" + jobID + ".opus"
	if got := string(files.files[remote]); got != "sealed bytes" {
		t.Fatalf("remote bytes = %q", got)
	}
	if _, wroteCatalog := files.files[ncDefaultRecordingsRoot+"/catalog.json"]; wroteCatalog {
		t.Fatal("wrote a remote catalog")
	}
	if len(created) != 2 || shares["alice"] != 17 || shares["bob"] != 17 {
		t.Fatalf("created=%v shares=%v", created, shares)
	}
	indexed, err := metadata.EntriesFor(ctx, []int64{42})
	if err != nil || !strings.Contains(string(indexed[42]), `"roomName":"Room One"`) {
		t.Fatalf("indexed=%s err=%v", indexed[42], err)
	}
	// A completed publish followed by a manual share removal is an audience
	// edit. Replacing the bytes must preserve that removal.
	if _, err := rt.store.db.ExecContext(ctx, `UPDATE job_attempts SET stage='done', state='succeeded' WHERE job_id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	delete(shares, "bob")
	createdBefore := len(created)
	if _, err := sink.Deliver(ctx, publishDelivery{AttemptSitePath: attempt, JobID: jobID}); err != nil {
		t.Fatal(err)
	}
	if len(created) != createdBefore || shares["bob"] != 0 {
		t.Fatalf("republish restored a removed share: created=%v shares=%v", created, shares)
	}
}
