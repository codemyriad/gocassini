package operator

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testExAppConfig(ncURL string) ExAppConfig {
	return ExAppConfig{
		NextcloudURL: ncURL,
		AppSecret:    "sekret",
		AppID:        "gocassini",
		AppVersion:   "1.2.3",
	}
}

type davRequest struct {
	method string
	path   string
	rng    string
	auth   string
	ctype  string
	body   []byte
}

func TestNCFilesUploaderMirrorsArchive(t *testing.T) {
	var mu sync.Mutex
	var got []davRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, davRequest{
			method: r.Method, path: r.URL.Path,
			auth:  r.Header.Get("AUTHORIZATION-APP-API"),
			ctype: r.Header.Get("Content-Type"),
			body:  body,
		})
		mu.Unlock()
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	siteRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(siteRoot, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	jobID := "01JOBULID"
	opusBytes := []byte("OPUSDATA")
	catalogBytes := []byte(`{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	if err := os.WriteFile(filepath.Join(siteRoot, "meetings", jobID+".opus"), opusBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), catalogBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	up := testExAppConfig(srv.URL).ncFilesUploader()
	if up == nil {
		t.Fatal("uploader nil with full ExApp config")
	}
	if err := up(context.Background(), siteRoot, jobID); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Expected auth header attributes the call to the admin owner.
	wantAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":sekret"))

	mkcols := map[string]bool{}
	puts := map[string][]byte{}
	for _, req := range got {
		if req.auth != wantAuth {
			t.Errorf("auth header = %q, want %q (path %s)", req.auth, wantAuth, req.path)
		}
		switch req.method {
		case "MKCOL":
			mkcols[req.path] = true
		case http.MethodPut:
			puts[req.path] = req.body
			if strings.HasSuffix(req.path, ".opus") && req.ctype != "audio/ogg" {
				t.Errorf("opus PUT Content-Type = %q, want audio/ogg", req.ctype)
			}
			if strings.HasSuffix(req.path, "catalog.json") && req.ctype != "application/json" {
				t.Errorf("catalog PUT Content-Type = %q, want application/json", req.ctype)
			}
		}
	}

	base := "/remote.php/dav/files/" + ncRecordingsOwner + "/" + ncRecordingsRoot
	for _, dir := range []string{"/remote.php/dav/files/" + ncRecordingsOwner + "/Cassini", base, base + "/meetings"} {
		if !mkcols[dir] {
			t.Errorf("missing MKCOL for %s (got %v)", dir, mkcols)
		}
	}
	if b, ok := puts[base+"/meetings/"+jobID+".opus"]; !ok || string(b) != string(opusBytes) {
		t.Errorf("opus PUT missing/wrong: ok=%v body=%q", ok, b)
	}
	if b, ok := puts[base+"/catalog.json"]; !ok || string(b) != string(catalogBytes) {
		t.Errorf("catalog PUT missing/wrong: ok=%v body=%q", ok, b)
	}
}

func TestNCFilesUploaderSkipsMissingOpus(t *testing.T) {
	var mu sync.Mutex
	puts := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			mu.Lock()
			puts[r.URL.Path] = true
			mu.Unlock()
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	siteRoot := t.TempDir() // no meetings/<jobID>.opus present
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := testExAppConfig(srv.URL).ncFilesUploader()
	if err := up(context.Background(), siteRoot, "01MISSING"); err != nil {
		t.Fatalf("upload should not fail on missing opus: %v", err)
	}
	if puts[filepath.Join("/remote.php/dav/files/"+ncRecordingsOwner+"/"+ncRecordingsRoot+"/meetings", "01MISSING.opus")] {
		t.Error("should not PUT a non-existent opus")
	}
	if !puts["/remote.php/dav/files/"+ncRecordingsOwner+"/"+ncRecordingsRoot+"/catalog.json"] {
		t.Error("catalog.json should still be uploaded")
	}
}

func TestNCFilesProxyRelaysAndForwardsRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/catalog.json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/7")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("CATA"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("CATALOG"))
	}))
	defer srv.Close()

	proxy := testExAppConfig(srv.URL).ncFilesProxy()
	if proxy == nil {
		t.Fatal("proxy nil with full ExApp config")
	}

	// Full GET.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/published/catalog.json", nil)
	if !proxy(rec, req, "catalog.json") {
		t.Fatal("proxy should have served catalog.json")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "CATALOG" {
		t.Errorf("full GET: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Range GET is forwarded and 206 relayed.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/published/catalog.json", nil)
	req.Header.Set("Range", "bytes=0-3")
	if !proxy(rec, req, "catalog.json") {
		t.Fatal("proxy should have served the ranged request")
	}
	if rec.Code != http.StatusPartialContent {
		t.Errorf("range GET code = %d, want 206", rec.Code)
	}
	if rec.Header().Get("Content-Range") != "bytes 0-3/7" {
		t.Errorf("Content-Range not relayed: %q", rec.Header().Get("Content-Range"))
	}
	if rec.Body.String() != "CATA" {
		t.Errorf("range body = %q", rec.Body.String())
	}
}

func TestNCFilesProxyFallsBackOnMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	proxy := testExAppConfig(srv.URL).ncFilesProxy()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/published/meetings/nope.opus", nil)
	if proxy(rec, req, "meetings/nope.opus") {
		t.Fatal("proxy should return false (fall back) on 404")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("proxy must not write a body when falling back, got %q", rec.Body.String())
	}
}

func TestNCFilesHooksNilWithoutExAppEnv(t *testing.T) {
	// Missing NextcloudURL (and secret/id) -> dev/standalone: no NC delivery.
	cfg := ExAppConfig{}
	if cfg.ncFilesUploader() != nil {
		t.Error("uploader should be nil without ExApp env")
	}
	if cfg.ncFilesProxy() != nil {
		t.Error("proxy should be nil without ExApp env")
	}
	// Secret present but NextcloudURL absent is still inactive.
	cfg = ExAppConfig{AppSecret: "s", AppID: "gocassini"}
	if cfg.ncFilesUploader() != nil || cfg.ncFilesProxy() != nil {
		t.Error("hooks should be nil without NextcloudURL")
	}
}

func TestDavFileURLEscapesSegments(t *testing.T) {
	cfg := testExAppConfig("https://nc.example.com/")
	got := cfg.davFileURL("admin", "Cassini/Recordings/catalog.json")
	want := "https://nc.example.com/remote.php/dav/files/admin/Cassini/Recordings/catalog.json"
	if got != want {
		t.Errorf("davFileURL = %q, want %q", got, want)
	}
}
