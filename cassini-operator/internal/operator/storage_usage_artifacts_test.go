package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetailedStorageUsageCombinesPublishedAndDirectoryFormats(t *testing.T) {
	workRoot := t.TempDir()
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "one.run", "camera.MKV"), 11)
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "two.run", "audio.webm"), 7)
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "metadata"), 3)
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "one.opus"), 13)
	writeUsageFile(t, filepath.Join(runsRoot(workRoot), "one.logs", "record.log"), 5)
	writeUsageFile(t, filepath.Join(runsRoot(workRoot), "one.seal", "one.opus"), 17)

	nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		root := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/files/cassini/")
		switch root {
		case "CassiniNoACL/Recordings":
			_, _ = w.Write([]byte(davSizesXML(root, true, 0, root+"/default.opus", false, 23)))
		case "Cassini/Recordings":
			_, _ = w.Write([]byte(davSizesXML(root, true, 0, root+"/controlled.opus", false, 29)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nc.Close()

	response := testExAppConfig(nc.URL).scanDetailedStorageUsage(t.Context(), workRoot)
	assertPublishedRoot(t, response, "default", 23)
	assertPublishedRoot(t, response, "access-controlled", 29)
	current := assertArtifactRoot(t, response, "current", 34, 4)
	assertArtifactFormat(t, current, ".opus", 13, 1)
	assertArtifactFormat(t, current, ".mkv", 11, 1)
	assertArtifactFormat(t, current, ".webm", 7, 1)
	assertArtifactFormat(t, current, "none", 3, 1)
	runs := assertArtifactRoot(t, response, "runs", 22, 2)
	assertArtifactFormat(t, runs, ".opus", 17, 1)
	assertArtifactFormat(t, runs, ".log", 5, 1)
	if response.DurationMS <= 0 || response.MeasuredAt == "" {
		t.Fatalf("calculation metadata = %+v", response)
	}
}

func TestDetailedStorageUsageTreatsMissingLocalRootsAsEmpty(t *testing.T) {
	response := ExAppConfig{}.scanDetailedStorageUsage(t.Context(), filepath.Join(t.TempDir(), "missing"))
	for _, root := range response.Directories {
		if root.Error != "" || root.Bytes != 0 || len(root.Formats) != 0 {
			t.Fatalf("missing root = %+v, want empty", root)
		}
	}
}

func TestDetailedStorageUsageDoesNotFollowSymlinks(t *testing.T) {
	workRoot := t.TempDir()
	target := filepath.Join(t.TempDir(), "large.opus")
	writeUsageFile(t, target, 99)
	if err := os.MkdirAll(currentRoot(workRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(currentRoot(workRoot), "linked.opus")); err != nil {
		t.Fatal(err)
	}
	root := assertArtifactRoot(t, ExAppConfig{}.scanDetailedStorageUsage(t.Context(), workRoot), "current", 0, 0)
	if len(root.Formats) != 0 {
		t.Fatalf("symlink produced formats: %+v", root.Formats)
	}
}

func TestDetailedStorageUsageIndexOnlyChangesOnPOST(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	name := filepath.Join(currentRoot(rt.cfg.WorkRoot), "one.run", "recording.mkv")
	writeUsageFile(t, name, 11)
	handler := ExAppConfig{}.detailedStorageUsageHandler(rt)

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/storage/usage/details", nil))
	var response detailedStorageUsageResponse
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.MeasuredAt != "" || len(response.Directories) != 0 || len(response.Published) != 0 {
		t.Fatalf("initial response = %+v", response)
	}

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/storage/usage/details", nil))
	if err := json.Unmarshal(post.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertArtifactRoot(t, response, "current", 11, 1)

	writeUsageFile(t, name, 29)
	get = httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/storage/usage/details", nil))
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertArtifactRoot(t, response, "current", 11, 1)

	post = httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/storage/usage/details", nil))
	if err := json.Unmarshal(post.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertArtifactRoot(t, response, "current", 29, 1)
}

func assertPublishedRoot(t *testing.T, response detailedStorageUsageResponse, id string, bytes int64) {
	t.Helper()
	for _, root := range response.Published {
		if root.ID == id {
			if root.Bytes != bytes || root.Error != "" {
				t.Fatalf("published root %s = %+v, want %d bytes", id, root, bytes)
			}
			return
		}
	}
	t.Fatalf("missing published root %q", id)
}

func assertArtifactRoot(t *testing.T, response detailedStorageUsageResponse, id string, bytes int64, files int) artifactStorageUsageRoot {
	t.Helper()
	for _, root := range response.Directories {
		if root.ID == id {
			if root.Bytes != bytes || root.Files != files || root.Error != "" {
				t.Fatalf("root %s = %+v, want %d bytes and %d files", id, root, bytes, files)
			}
			return root
		}
	}
	t.Fatalf("missing root %q in %+v", id, response.Directories)
	return artifactStorageUsageRoot{}
}

func assertArtifactFormat(t *testing.T, root artifactStorageUsageRoot, extension string, bytes int64, files int) {
	t.Helper()
	for _, format := range root.Formats {
		if format.Extension == extension {
			if format.Bytes != bytes || format.Files != files {
				t.Fatalf("format %s = %+v, want %d bytes and %d files", extension, format, bytes, files)
			}
			return
		}
	}
	t.Fatalf("missing format %q in %+v", extension, root.Formats)
}
