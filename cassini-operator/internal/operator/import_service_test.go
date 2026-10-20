package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

// importHandlerService builds the HTTP surface over a real jobs database with
// no worker behind it, so handler behavior is asserted without running imports.
func importHandlerService(t *testing.T) *importService {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	rt := &Runtime{store: store, logger: log.New(testWriter{t}, "", 0)}
	return &importService{rt: rt, logger: rt.logger}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func importDo(t *testing.T, s *importService, method, target, caller, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if caller != "" {
		req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	}
	mux := http.NewServeMux()
	s.register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestCreateImportRefusesABadRequest(t *testing.T) {
	s := importHandlerService(t)
	cases := []struct {
		name, body string
		want       int
	}{
		{"no files", `{"files":[]}`, 400},
		{"unknown field", `{"files":[{"stagedPath":".CassiniImports/a.opus"}],"mode":"x"}`, 400},
		{"path outside staging", `{"files":[{"stagedPath":"Documents/a.opus"}]}`, 400},
		{"path escaping staging", `{"files":[{"stagedPath":".CassiniImports/../../x"}]}`, 400},
		{"nested path", `{"files":[{"stagedPath":".CassiniImports/deep/a.opus"}]}`, 400},
		{"bad principal type", `{"files":[{"stagedPath":".CassiniImports/a.opus","share":true,"audience":[{"type":"team","id":"x"}]}]}`, 400},
		{"empty principal id", `{"files":[{"stagedPath":".CassiniImports/a.opus","share":true,"audience":[{"type":"user","id":" "}]}]}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := importDo(t, s, http.MethodPost, importsURLPath, "alice", tc.body)
			if w.Code != tc.want {
				t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
	// Nothing was enqueued by any refusal.
	jobs, err := s.rt.store.ImportJobsFor(context.Background(), "alice")
	if err != nil || len(jobs) != 0 {
		t.Fatalf("refusals enqueued work: %v %v", jobs, err)
	}
}

func TestImportLifecycleOverHTTP(t *testing.T) {
	s := importHandlerService(t)
	body := `{"files":[
	  {"stagedPath":"/.CassiniImports/abc-retro.opus","sourceName":"retro.opus","share":true,"audience":[{"type":"user","id":"mina"},{"type":"group","id":"growth"}]},
	  {"stagedPath":".CassiniImports/def-standup.json","share":false}
	]}`
	w := importDo(t, s, http.MethodPost, importsURLPath, "alice", body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		BatchID string      `json:"batchId"`
		Imports []ImportJob `json:"imports"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Imports) != 2 || created.Imports[0].State != importStateQueued || created.Imports[0].SourceName != "retro.opus" {
		t.Fatalf("created: %+v", created)
	}
	if got := created.Imports[0].Sharing.Audience; len(got) != 2 || got[0].ID != "mina" {
		t.Fatalf("sharing lost: %+v", got)
	}
	if created.Imports[1].SourceName != "def-standup.json" || created.Imports[1].Sharing.Share {
		t.Fatalf("second file: %+v", created.Imports[1])
	}
	id := created.Imports[0].ID

	// The creator reads it; anyone else gets a 404, not a 403.
	if w := importDo(t, s, http.MethodGet, importsURLPath+"/"+id, "alice", ""); w.Code != 200 {
		t.Fatalf("own read: %d", w.Code)
	}
	if w := importDo(t, s, http.MethodGet, importsURLPath+"/"+id, "mallory", ""); w.Code != 404 {
		t.Fatalf("stranger read: %d", w.Code)
	}
	if w := importDo(t, s, http.MethodGet, importsURLPath, "mallory", ""); !strings.Contains(w.Body.String(), `"imports":[]`) {
		t.Fatalf("stranger list: %s", w.Body.String())
	}

	// Cancel, then retry, over the API.
	if w := importDo(t, s, http.MethodPost, importsURLPath+"/"+id+"/cancel", "alice", ""); w.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d", w.Code)
	}
	if job, _, _ := s.rt.store.ImportJob(context.Background(), id); job.State != importStateCanceled {
		t.Fatalf("cancel state: %s", job.State)
	}
	if w := importDo(t, s, http.MethodPost, importsURLPath+"/"+id+"/retry", "alice", ""); w.Code != http.StatusAccepted {
		t.Fatalf("retry: %d", w.Code)
	}
	if job, _, _ := s.rt.store.ImportJob(context.Background(), id); job.State != importStateQueued {
		t.Fatalf("retry state: %s", job.State)
	}

	// No identity at all is an outage, not an empty answer.
	if w := importDo(t, s, http.MethodGet, importsURLPath, "", ""); w.Code != http.StatusBadGateway {
		t.Fatalf("anonymous: %d", w.Code)
	}
}

func TestSanitizeStagedImportPath(t *testing.T) {
	good := []string{".CassiniImports/a.opus", "/.CassiniImports/b c.json"}
	for _, raw := range good {
		if got, err := sanitizeStagedImportPath(raw); err != nil || !strings.HasPrefix(got, importStagingDir+"/") {
			t.Fatalf("%q: %q %v", raw, got, err)
		}
	}
	bad := []string{"", "a.opus", "Documents/a.opus", ".CassiniImports/", ".CassiniImports/..", ".CassiniImports/x/y", "CassiniImports/a.opus"}
	for _, raw := range bad {
		if got, err := sanitizeStagedImportPath(raw); err == nil {
			t.Fatalf("%q accepted as %q", raw, got)
		}
	}
}

func TestCLIImportsRejectWebRecoveryAndRemainVisible(t *testing.T) {
	s := importHandlerService(t)
	ctx := context.Background()
	job := ImportJob{ID: "cli", BatchID: "b", CreatedBy: "alice", SourceName: "source.opus", StagedPath: "local:/tmp/source.opus"}
	if err := s.rt.store.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
		t.Fatal(err)
	}
	if err := s.rt.store.SetImportJobState(ctx, job.ID, importStateFailed, "", "CLI validation failed"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"retry", "cancel"} {
		w := importDo(t, s, "POST", "/imports/cli/"+action, "alice", "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "local CLI file") {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
		if w := importDo(t, s, "POST", "/imports/cli/"+action, "mallory", ""); w.Code != http.StatusNotFound {
			t.Fatal(w.Code)
		}
	}
	w := importDo(t, s, "GET", "/imports", "alice", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"origin":"cli"`) || !strings.Contains(w.Body.String(), "CLI validation failed") {
		t.Fatal(w.Code, w.Body.String())
	}
	// Defense in depth: even a direct worker call must refuse the local source
	// before attempting DAV or touching local files. This service has no client.
	if _, err := s.performImport(ctx, job); err == nil || !strings.Contains(err.Error(), "local CLI file") {
		t.Fatal(err)
	}
}
