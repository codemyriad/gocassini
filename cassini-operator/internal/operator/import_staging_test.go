package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stagingFixture(t *testing.T) (*importService, map[string]int64, *[]string) {
	t.Helper()
	s := importHandlerService(t)
	files := map[string]int64{}
	deleted := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ocs/v2.php/cloud/users") {
			fmt.Fprint(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["alice","bob"]}}}`)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/files/")
		switch r.Method {
		case "PROPFIND":
			if r.Header.Get("Depth") == "1" {
				w.WriteHeader(207)
				fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`)
				for file, size := range files {
					if strings.HasPrefix(file, strings.TrimSuffix(path, "/")+"/") {
						fmt.Fprintf(w, `<d:response><d:href>/remote.php/dav/files/%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, file, size)
					}
				}
				fmt.Fprint(w, `</d:multistatus>`)
				return
			}
			size, ok := files[path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, size)
		case "DELETE":
			if strings.Contains(path, "refused") {
				w.WriteHeader(503)
				return
			}
			deleted = append(deleted, path)
			delete(files, path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	s.exapp = testExAppConfig(server.URL)
	s.client = server.Client()
	return s, files, &deleted
}

func prepareUpload(t *testing.T, s *importService, user, name string) ImportJob {
	t.Helper()
	body := fmt.Sprintf(`{"prepare":true,"files":[{"stagedPath":".CassiniImports/%s","sourceName":"%s","expectedBytes":10,"share":false}]}`, name, name)
	w := importDo(t, s, "POST", "/imports", user, body)
	if w.Code != 202 {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Imports []ImportJob `json:"imports"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Imports[0]
}

func TestUploadTrackingSurvivesLostCompletionAndRestart(t *testing.T) {
	s, files, _ := stagingFixture(t)
	job := prepareUpload(t, s, "alice", "complete.opus")
	if job.State != importStateUploading {
		t.Fatal(job)
	}
	files["alice/"+job.StagedPath] = 10
	// Startup must not enqueue an upload just because it has a DB record.
	if _, err := s.rt.store.RequeueUnfinishedImports(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _, _ := s.rt.store.ImportJob(context.Background(), job.ID)
	if current.State != importStateUploading {
		t.Fatal(current.State)
	}
	// The PUT succeeded but the browser disappeared before confirmation.
	_, err := s.rt.store.db.Exec(`UPDATE import_jobs SET updated_at=? WHERE id=?`, time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339Nano), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := importDo(t, s, "GET", "/imports", "alice", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"staged"`) {
		t.Fatal(w.Body.String())
	}
	if w := importDo(t, s, "POST", "/imports/"+job.ID+"/retry", "alice", ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Repeated submission addresses the same row, not a second import.
	body := fmt.Sprintf(`{"files":[{"stagedPath":%q}]}`, job.StagedPath)
	if w := importDo(t, s, "POST", "/imports", "alice", body); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	jobs, _ := s.rt.store.ImportJobsFor(context.Background(), "alice")
	if len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].State != importStateQueued {
		t.Fatal(jobs)
	}
}

func TestPartialUploadCannotRetryAndRemovalIsDurable(t *testing.T) {
	s, files, _ := stagingFixture(t)
	job := prepareUpload(t, s, "alice", "partial.opus")
	files["alice/"+job.StagedPath] = 3
	if w := importDo(t, s, "POST", "/imports/"+job.ID+"/staged", "alice", ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := importDo(t, s, "POST", "/imports/"+job.ID+"/retry", "alice", ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := importDo(t, s, "POST", "/imports/"+job.ID+"/remove", "bob", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := importDo(t, s, "POST", "/imports/"+job.ID+"/remove", "alice", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	jobs, _ := s.rt.store.ImportJobsFor(context.Background(), "alice")
	if len(jobs) != 0 || len(files) != 0 {
		t.Fatal(jobs, files)
	}
	if w := importDo(t, s, "POST", "/imports/"+job.ID+"/retry", "alice", ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestStagingSweepAllUsersPreservesActiveAndSuccessfulHistory(t *testing.T) {
	s, files, _ := stagingFixture(t)
	active := prepareUpload(t, s, "alice", "active.opus")
	failed := prepareUpload(t, s, "alice", "failed.opus")
	done := prepareUpload(t, s, "alice", "done.opus")
	refused := prepareUpload(t, s, "alice", "refused.opus")
	expired := prepareUpload(t, s, "alice", "expired.opus")
	files["alice/"+expired.StagedPath] = 3
	s.rt.store.db.Exec(`UPDATE import_jobs SET updated_at=? WHERE id=?`, time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339Nano), expired.ID)
	for _, job := range []ImportJob{active, failed, done, refused} {
		files["alice/"+job.StagedPath] = 10
	}
	ctx := context.Background()
	s.rt.store.SetImportJobState(ctx, failed.ID, importStateUploadFailed, "", "failed upload")
	s.rt.store.SetImportJobState(ctx, refused.ID, importStateUploadFailed, "", "failed upload")
	s.rt.store.SetImportJobOutcome(ctx, done.ID, "done.opus", 12, nowUTCString())
	// Bob has an old orphan but no DB records at all.
	files["bob/.CassiniImports/orphan.opus"] = 4
	if err := s.sweepImportStaging(ctx); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files["alice/"+active.StagedPath] != 10 || files["alice/"+refused.StagedPath] != 10 {
		t.Fatal(files)
	}
	// Files go; rows stay. The sweep hides nothing — a failed row keeps its
	// error on the list until its owner removes it, only without a Retry.
	jobs, _ := s.rt.store.ImportJobsFor(ctx, "alice")
	if len(jobs) != 5 {
		t.Fatal(jobs)
	}
	current, _, _ := s.rt.store.ImportJob(ctx, done.ID)
	if current.State != importStateDone || current.StagingState != "removed" || current.ClearedAt != "" {
		t.Fatal(current)
	}
	swept, _, _ := s.rt.store.ImportJob(ctx, failed.ID)
	if swept.State != importStateUploadFailed || swept.StagingState != "removed" || swept.ClearedAt != "" || swept.Error == "" {
		t.Fatal(swept)
	}
	if w := importDo(t, s, "POST", "/imports/"+failed.ID+"/retry", "alice", ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	// The explicit Remove is what hides it.
	if w := importDo(t, s, "POST", "/imports/"+failed.ID+"/remove", "alice", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if jobs, _ := s.rt.store.ImportJobsFor(ctx, "alice"); len(jobs) != 4 {
		t.Fatal(jobs)
	}
	deliveries, _ := s.rt.store.doneImportDeliveries(ctx)
	if len(deliveries) != 1 || deliveries[0] != "done.opus" {
		t.Fatal(deliveries)
	}
	// Removing a live upload must not race its PUT.
	if w := importDo(t, s, "POST", "/imports/"+active.ID+"/remove", "alice", ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestNextStagingSweepIs0303UTC(t *testing.T) {
	for _, tc := range []struct{ now, want string }{
		{"2026-10-20T03:02:59Z", "2026-10-20T03:03:00Z"},
		{"2026-10-20T03:03:00Z", "2026-10-21T03:03:00Z"},
		{"2026-10-20T05:02:00+02:00", "2026-10-20T03:03:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := nextImportStagingSweep(now).Format(time.RFC3339); got != tc.want {
			t.Fatalf("%s: %s != %s", tc.now, got, tc.want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A Nextcloud that is slow for one user's staging work must not stall another
// user's list or registration: staging is serialized per user, never across
// users, and the service mutex is held only around the worker's running job.
func TestStagingWorkForOneUserDoesNotBlockAnother(t *testing.T) {
	s, files, _ := stagingFixture(t)
	ctx := context.Background()
	bob := prepareUpload(t, s, "bob", "slow.opus")
	files["bob/"+bob.StagedPath] = 10
	s.rt.store.SetImportJobState(ctx, bob.ID, importStateUploadFailed, "", "failed upload")

	gate := make(chan struct{})
	inFlight := make(chan struct{}, 1)
	base := s.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/files/bob/") {
			select {
			case inFlight <- struct{}{}:
			default:
			}
			<-gate
		}
		return base.RoundTrip(r)
	})}
	cleared := make(chan error, 1)
	go func() { cleared <- s.clearUserStaging(ctx, "bob") }()
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("bob's DELETE never started")
	}
	defer func() { close(gate); <-cleared }()

	started := time.Now()
	if w := importDo(t, s, "GET", "/imports", "alice", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	alice := prepareUpload(t, s, "alice", "quick.opus")
	if w := importDo(t, s, "POST", "/imports/"+alice.ID+"/heartbeat", "alice", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("alice waited %s behind bob's staging work", waited)
	}
}
