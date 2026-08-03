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
		AAVersion:    "34.0.0",
	}
}

type davRequest struct {
	method string
	path   string
	rng    string
	auth   string
	ctype  string
	aa     string
	ocs    string
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
			aa:    r.Header.Get("AA-VERSION"),
			ocs:   r.Header.Get("OCS-APIRequest"),
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
	opusFiles := map[string][]byte{
		"01FIRST.opus":  []byte("FIRST"),
		"01SECOND.opus": []byte("SECOND"),
	}
	catalogBytes := []byte(`{"version":"cassini.viewer.catalog.v1","meetings":[]}`)
	for name, contents := range opusFiles {
		if err := os.WriteFile(filepath.Join(siteRoot, "meetings", name), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "meetings", "ignore.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), catalogBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	up := testExAppConfig(srv.URL).ncFilesUploader(nil)
	if up == nil {
		t.Fatal("uploader nil with full ExApp config")
	}
	if err := up(context.Background(), siteRoot); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Expected auth header attributes the call to the admin owner.
	wantAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":sekret"))

	mkcols := map[string]bool{}
	puts := map[string][]byte{}
	putOrder := []string{}
	for _, req := range got {
		if req.auth != wantAuth {
			t.Errorf("auth header = %q, want %q (path %s)", req.auth, wantAuth, req.path)
		}
		if req.aa != "34.0.0" {
			t.Errorf("AA-VERSION = %q, want 34.0.0 (path %s)", req.aa, req.path)
		}
		if req.ocs != "" {
			t.Errorf("OCS-APIRequest = %q, want empty for DAV (path %s)", req.ocs, req.path)
		}
		switch req.method {
		case "MKCOL":
			mkcols[req.path] = true
		case http.MethodPut:
			puts[req.path] = req.body
			putOrder = append(putOrder, req.path)
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
	for name, want := range opusFiles {
		if b, ok := puts[base+"/meetings/"+name]; !ok || string(b) != string(want) {
			t.Errorf("opus PUT %s missing/wrong: ok=%v body=%q", name, ok, b)
		}
	}
	if _, ok := puts[base+"/meetings/ignore.txt"]; ok {
		t.Error("non-opus file should not be uploaded")
	}
	if b, ok := puts[base+"/catalog.json"]; !ok || string(b) != string(catalogBytes) {
		t.Errorf("catalog PUT missing/wrong: ok=%v body=%q", ok, b)
	}
	if len(putOrder) != 3 || putOrder[len(putOrder)-1] != base+"/catalog.json" {
		t.Errorf("catalog must be PUT after every opus, order=%v", putOrder)
	}
}

func TestNCFilesUploaderPublishesEmptyCatalog(t *testing.T) {
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

	siteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(siteRoot, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := testExAppConfig(srv.URL).ncFilesUploader(nil)
	if err := up(context.Background(), siteRoot); err != nil {
		t.Fatalf("upload empty catalog: %v", err)
	}
	if len(puts) != 1 || !puts["/remote.php/dav/files/"+ncRecordingsOwner+"/"+ncRecordingsRoot+"/catalog.json"] {
		t.Errorf("only catalog.json should be uploaded, got %v", puts)
	}
}

func TestNCFilesUploaderProtectsCatalogWhenAccessControlEnabled(t *testing.T) {
	var got []davRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, davRequest{method: r.Method, path: r.URL.Path, body: body})
		switch r.Method {
		case "PROPPATCH":
			w.WriteHeader(http.StatusMultiStatus)
			return
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"/>`)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	siteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(siteRoot, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "meetings", "new.opus"), []byte("OPUS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), []byte(`{"meetings":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	if err := cfg.ncFilesUploader(nil)(context.Background(), siteRoot); err != nil {
		t.Fatalf("upload: %v", err)
	}

	var catalogPut, catalogACL, opusPut, opusACL int
	var protectedOpusBeforeCatalog bool
	for _, req := range got {
		switch {
		case strings.HasSuffix(req.path, "/new.opus") && req.method == http.MethodPut:
			opusPut++
		case strings.HasSuffix(req.path, "/new.opus") && req.method == "PROPPATCH":
			opusACL++
			body := string(req.body)
			if !strings.Contains(body, "everyone") || !strings.Contains(body, "<nc:acl-permissions>0</nc:acl-permissions>") {
				t.Errorf("new opus baseline ACL does not deny the traversal group: %s", body)
			}
		case strings.HasSuffix(req.path, "/catalog.json") && req.method == http.MethodPut:
			catalogPut++
			protectedOpusBeforeCatalog = opusACL == 1
		case strings.HasSuffix(req.path, "/catalog.json") && req.method == "PROPPATCH":
			catalogACL++
			body := string(req.body)
			for _, want := range []string{
				"everyone",
				"<nc:acl-permissions>0</nc:acl-permissions>",
				"<nc:acl-mapping-id>admin</nc:acl-mapping-id>",
				"<nc:acl-permissions>31</nc:acl-permissions>",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("catalog ACL body missing %q: %s", want, body)
				}
			}
		}
	}
	if catalogPut != 1 || catalogACL != 1 || opusPut != 1 || opusACL != 1 {
		t.Fatalf("protected requests: opus PUT=%d ACL=%d catalog PUT=%d ACL=%d, want one each; got %+v", opusPut, opusACL, catalogPut, catalogACL, got)
	}
	if !protectedOpusBeforeCatalog {
		t.Fatal("new opus must receive its deny baseline before catalog.json is uploaded")
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

	proxy := testExAppConfig(srv.URL).ncFilesProxy(nil)
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
	if got := rec.Header().Get(ncFilesSourceHeader); got != ncFilesSourceValue {
		t.Errorf("source header = %q, want %q", got, ncFilesSourceValue)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("catalog Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("catalog Content-Type = %q, want application/json", got)
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

func TestNCFilesProxyMakesFilesMissAuthoritative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	proxy := testExAppConfig(srv.URL).ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/published/meetings/nope.opus", nil)
	if !proxy(rec, req, "meetings/nope.opus") {
		t.Fatal("configured Files proxy must handle a miss without local fallback")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("proxy miss code = %d, want 404", rec.Code)
	}
}

func TestNCFilesUploaderDoesNotPublishCatalogAfterOpusFailure(t *testing.T) {
	var catalogPut bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/broken.opus") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/catalog.json") {
			catalogPut = true
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	siteRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(siteRoot, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "meetings", "broken.opus"), []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testExAppConfig(srv.URL).ncFilesUploader(nil)(context.Background(), siteRoot); err == nil {
		t.Fatal("expected opus upload failure")
	}
	if catalogPut {
		t.Fatal("catalog must not be published after an opus upload fails")
	}
}

func TestNCFilesProxyReturnsBadGatewayWhenFilesUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	proxy := testExAppConfig(url).ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/published/catalog.json", nil)
	if !proxy(rec, req, "catalog.json") {
		t.Fatal("configured Files proxy must handle an upstream failure")
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("unavailable proxy code = %d, want 502", rec.Code)
	}
}

func TestNCFilesHooksNilWithoutExAppEnv(t *testing.T) {
	// Missing NextcloudURL (and secret/id) -> dev/standalone: no NC delivery.
	cfg := ExAppConfig{}
	if cfg.ncFilesUploader(nil) != nil {
		t.Error("uploader should be nil without ExApp env")
	}
	if cfg.ncFilesProxy(nil) != nil {
		t.Error("proxy should be nil without ExApp env")
	}
	// Secret present but NextcloudURL absent is still inactive.
	cfg = ExAppConfig{AppSecret: "s", AppID: "gocassini"}
	if cfg.ncFilesUploader(nil) != nil || cfg.ncFilesProxy(nil) != nil {
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
