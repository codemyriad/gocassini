package operator

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// The relay path is meetings/<id>.opus: catalog.json is built per caller rather
// than streamed (webdav_read_test.go covers that), so this is where byte
// relaying and Range forwarding actually happen.
func TestNCFilesProxyRelaysAndForwardsRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/meetings/demo.opus") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/7")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("OPUS"))
			return
		}
		w.Header().Set("Content-Type", "audio/ogg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OPUSBYTES"))
	}))
	defer srv.Close()

	proxy := testExAppConfig(srv.URL).ncFilesProxy(nil)
	if proxy == nil {
		t.Fatal("proxy nil with full ExApp config")
	}

	// Full GET.
	rec := httptest.NewRecorder()
	req := callerReq(http.MethodGet, "/published/meetings/demo.opus", "alice")
	if !proxy(rec, req, "meetings/demo.opus") {
		t.Fatal("proxy should have served the recording")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "OPUSBYTES" {
		t.Errorf("full GET: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(ncFilesSourceHeader); got != ncFilesSourceValue {
		t.Errorf("source header = %q, want %q", got, ncFilesSourceValue)
	}

	// Range GET is forwarded and 206 relayed.
	rec = httptest.NewRecorder()
	req = callerReq(http.MethodGet, "/published/meetings/demo.opus", "alice")
	req.Header.Set("Range", "bytes=0-3")
	if !proxy(rec, req, "meetings/demo.opus") {
		t.Fatal("proxy should have served the ranged request")
	}
	if rec.Code != http.StatusPartialContent {
		t.Errorf("range GET code = %d, want 206", rec.Code)
	}
	if rec.Header().Get("Content-Range") != "bytes 0-3/7" {
		t.Errorf("Content-Range not relayed: %q", rec.Header().Get("Content-Range"))
	}
	if rec.Body.String() != "OPUS" {
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
	req := callerReq(http.MethodGet, "/published/meetings/nope.opus", "alice")
	if !proxy(rec, req, "meetings/nope.opus") {
		t.Fatal("configured Files proxy must handle a miss without local fallback")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("proxy miss code = %d, want 404", rec.Code)
	}
}

func TestNCFilesProxyReturnsBadGatewayWhenFilesUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	proxy := testExAppConfig(url).ncFilesProxy(nil)
	rec := httptest.NewRecorder()
	// A recording rather than the catalog: an unreachable Files must surface as
	// an outage, where the catalog deliberately fails closed to an empty list.
	req := callerReq(http.MethodGet, "/published/meetings/x.opus", "alice")
	if !proxy(rec, req, "meetings/x.opus") {
		t.Fatal("configured Files proxy must handle an upstream failure")
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("unavailable proxy code = %d, want 502", rec.Code)
	}
}

func TestNCFilesHooksNilWithoutExAppEnv(t *testing.T) {
	// Missing NextcloudURL (and secret/id) -> dev/standalone: no NC delivery.
	cfg := ExAppConfig{}
	if cfg.ncFilesProxy(nil) != nil {
		t.Error("proxy should be nil without ExApp env")
	}
	// Secret present but NextcloudURL absent is still inactive.
	cfg = ExAppConfig{AppSecret: "s", AppID: "gocassini"}
	if cfg.ncFilesProxy(nil) != nil {
		t.Error("proxy should be nil without NextcloudURL")
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
