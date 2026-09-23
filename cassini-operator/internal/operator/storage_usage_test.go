package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStorageUsageReportsRecordingAndBuildLocations(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetStorageMode(t)
	setStorageMode(t, false)

	writeUsageFile(t, filepath.Join(currentRoot(rt.cfg.WorkRoot), "one.run", "recording.mkv"), 11)
	writeUsageFile(t, filepath.Join(runsRoot(rt.cfg.WorkRoot), "one--attempt-001.site", "meeting.opus"), 17)
	writeUsageFile(t, filepath.Join(rt.cfg.SiteRoot, "meetings", "legacy.opus"), 19)

	nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Errorf("method = %s, want PROPFIND", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/remote.php/dav/files/cassini/CassiniNoACL/Recordings":
			_, _ = w.Write([]byte(davSizesXML(
				"CassiniNoACL/Recordings", true, 0,
				"CassiniNoACL/Recordings/catalog.json", false, 5,
				"CassiniNoACL/Recordings/meetings", true, 0,
			)))
		case "/remote.php/dav/files/cassini/CassiniNoACL/Recordings/meetings":
			_, _ = w.Write([]byte(davSizesXML(
				"CassiniNoACL/Recordings/meetings", true, 0,
				"CassiniNoACL/Recordings/meetings/one.opus", false, 7,
			)))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nc.Close()

	response := testExAppConfig(nc.URL).scanStorageUsage(t.Context(), rt)
	published := assertUsageSource(t, response, "published", 12, "")
	assertUsageSource(t, response, "current", 11, "")
	assertUsageSource(t, response, "runs", 17, "")
	assertUsageSource(t, response, "legacy-site", 19, "")
	if published.Requests != 2 || published.Collections != 2 || published.Files != 2 {
		t.Fatalf("published diagnostics = %+v, want 2 requests, 2 collections and 2 files", published)
	}
	if published.DurationMS <= 0 || response.DurationMS < published.DurationMS {
		t.Fatalf("timings = response %.3fms, published %.3fms; want a measured request within the total", response.DurationMS, published.DurationMS)
	}
}

func TestStorageUsageKeepsSuccessfulRowsWhenNextcloudFails(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	writeUsageFile(t, filepath.Join(currentRoot(rt.cfg.WorkRoot), "one.run", "recording.mkv"), 11)

	nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer nc.Close()

	response := testExAppConfig(nc.URL).scanStorageUsage(t.Context(), rt)
	assertUsageSource(t, response, "published", 0, "PROPFIND")
	assertUsageSource(t, response, "current", 11, "")
}

func TestStorageUsageHandlerReadsAndRefreshesTheIndex(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	cfg := testExAppConfig("")

	rec := httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /storage/usage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body storageUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode storage usage: %v", err)
	}
	if body.MeasuredAt != "" || len(body.Sources) != 0 {
		t.Fatalf("initial response = %+v, want an empty in-memory index", body)
	}

	rec = httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/storage/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /storage/usage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refreshed storage usage: %v", err)
	}
	if body.MeasuredAt == "" || len(body.Sources) != 3 {
		t.Fatalf("refreshed response = %+v, want timestamp and the three regular sources", body)
	}

	rec = httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/storage/usage", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /storage/usage = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q, want GET, POST", got)
	}

	found := false
	for _, route := range operatorAPIRoutes(rt, cfg) {
		if route.pattern == "/storage/usage" {
			found = true
		}
	}
	if !found {
		t.Fatal("operator API did not register /storage/usage")
	}
}

func TestStorageUsageGETDoesNotRescanFolders(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	cfg := testExAppConfig("")
	name := filepath.Join(currentRoot(rt.cfg.WorkRoot), "one.run", "recording.mkv")
	writeUsageFile(t, name, 11)

	post := httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/storage/usage", nil))
	var refreshed storageUsageResponse
	if err := json.Unmarshal(post.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	assertUsageSource(t, refreshed, "current", 11, "")

	writeUsageFile(t, name, 29)
	get := httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/storage/usage", nil))
	var cached storageUsageResponse
	if err := json.Unmarshal(get.Body.Bytes(), &cached); err != nil {
		t.Fatal(err)
	}
	assertUsageSource(t, cached, "current", 11, "")

	post = httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/storage/usage", nil))
	if err := json.Unmarshal(post.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	assertUsageSource(t, refreshed, "current", 29, "")
}

// The page calls the ExApp through its mounted operator path, not its internal
// mux. Keep that path covered so a route-table change cannot leave the tab
// working in a unit test while every deployed request returns 404.
func TestStorageUsageIsRoutedUnderTheOperatorBasePath(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.BasePath = "/operator"

	handler := newHTTPHandler(discardLogger(), rt, ExAppConfig{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/operator/storage/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /operator/storage/usage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func writeUsageFile(t *testing.T, name string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertUsageSource(t *testing.T, response storageUsageResponse, id string, bytes int64, errorPart string) storageUsageSource {
	t.Helper()
	for _, source := range response.Sources {
		if source.ID != id {
			continue
		}
		if source.Bytes != bytes {
			t.Fatalf("%s bytes = %d, want %d", id, source.Bytes, bytes)
		}
		if errorPart == "" && source.Error != "" {
			t.Fatalf("%s error = %q, want none", id, source.Error)
		}
		if errorPart != "" && !strings.Contains(source.Error, errorPart) {
			t.Fatalf("%s error = %q, want it to contain %q", id, source.Error, errorPart)
		}
		return source
	}
	t.Fatalf("response did not contain source %q: %+v", id, response.Sources)
	return storageUsageSource{}
}

func davSizesXML(entries ...any) string {
	var out string
	for i := 0; i < len(entries); i += 3 {
		rel := entries[i].(string)
		collection := entries[i+1].(bool)
		size := entries[i+2].(int)
		resourceType := ""
		if collection {
			resourceType = "<d:collection/>"
		}
		out += "<d:response><d:href>/remote.php/dav/files/cassini/" + rel + "</d:href><d:propstat><d:prop><d:resourcetype>" + resourceType + "</d:resourcetype><d:getcontentlength>" + strconv.Itoa(size) + "</d:getcontentlength></d:prop></d:propstat></d:response>"
	}
	return `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">` + out + `</d:multistatus>`
}
