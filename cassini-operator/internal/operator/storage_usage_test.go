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

	response := testExAppConfig(nc.URL).storageUsage(t.Context(), rt)
	assertUsageSource(t, response, "published", 12, "")
	assertUsageSource(t, response, "current", 11, "")
	assertUsageSource(t, response, "runs", 17, "")
	assertUsageSource(t, response, "legacy-site", 19, "")
}

func TestStorageUsageKeepsSuccessfulRowsWhenNextcloudFails(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	writeUsageFile(t, filepath.Join(currentRoot(rt.cfg.WorkRoot), "one.run", "recording.mkv"), 11)

	nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer nc.Close()

	response := testExAppConfig(nc.URL).storageUsage(t.Context(), rt)
	assertUsageSource(t, response, "published", 0, "PROPFIND")
	assertUsageSource(t, response, "current", 11, "")
}

func TestStorageUsageHandlerAndRouteAreGetOnly(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	cfg := testExAppConfig("")

	rec := httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /storage/usage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body storageUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode storage usage: %v", err)
	}
	if body.MeasuredAt == "" || len(body.Sources) != 3 {
		t.Fatalf("response = %+v, want timestamp and the three regular sources", body)
	}

	rec = httptest.NewRecorder()
	cfg.storageUsageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/storage/usage", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /storage/usage = %d, want 405", rec.Code)
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

func assertUsageSource(t *testing.T, response storageUsageResponse, id string, bytes int64, errorPart string) {
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
		return
	}
	t.Fatalf("response did not contain source %q: %+v", id, response.Sources)
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
