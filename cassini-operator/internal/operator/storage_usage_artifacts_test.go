package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactStorageUsageBreaksRootsIntoEntriesAndFormats(t *testing.T) {
	workRoot := t.TempDir()
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "one.run", "camera.MKV"), 11)
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "one.run", "audio.webm"), 7)
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "one.run", "metadata"), 3)
	writeUsageFile(t, filepath.Join(currentRoot(workRoot), "one.opus"), 13)
	writeUsageFile(t, filepath.Join(runsRoot(workRoot), "one--attempt-001.logs", "record.log"), 5)
	writeUsageFile(t, filepath.Join(runsRoot(workRoot), "one--attempt-001.seal", "one.opus"), 17)

	response := scanArtifactStorageUsage(t.Context(), workRoot)
	current := assertArtifactRoot(t, response, "current", 34, 4)
	runs := assertArtifactRoot(t, response, "runs", 22, 2)
	run := assertArtifactItem(t, current, "one.run", 21)
	assertArtifactFormat(t, run, ".mkv", 11, 1)
	assertArtifactFormat(t, run, ".webm", 7, 1)
	assertArtifactFormat(t, run, "none", 3, 1)
	canonical := assertArtifactItem(t, current, "one.opus", 13)
	assertArtifactFormat(t, canonical, ".opus", 13, 1)
	seal := assertArtifactItem(t, runs, "one--attempt-001.seal", 17)
	assertArtifactFormat(t, seal, ".opus", 17, 1)
}

func TestArtifactStorageUsageTreatsMissingRootsAsEmpty(t *testing.T) {
	response := scanArtifactStorageUsage(t.Context(), filepath.Join(t.TempDir(), "missing"))
	for _, root := range response.Roots {
		if root.Error != "" || root.Bytes != 0 || len(root.Items) != 0 {
			t.Fatalf("missing root = %+v, want empty", root)
		}
	}
}

func TestArtifactStorageUsageDoesNotFollowSymlinks(t *testing.T) {
	workRoot := t.TempDir()
	target := filepath.Join(t.TempDir(), "large.opus")
	writeUsageFile(t, target, 99)
	if err := os.MkdirAll(currentRoot(workRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(currentRoot(workRoot), "linked.opus")); err != nil {
		t.Fatal(err)
	}
	root := assertArtifactRoot(t, scanArtifactStorageUsage(t.Context(), workRoot), "current", 0, 0)
	if len(root.Items) != 0 {
		t.Fatalf("symlink produced items: %+v", root.Items)
	}
}

func TestArtifactStorageUsageIndexOnlyChangesOnPOST(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	name := filepath.Join(currentRoot(rt.cfg.WorkRoot), "one.run", "recording.mkv")
	writeUsageFile(t, name, 11)
	handler := artifactStorageUsageHandler(rt)

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/storage/usage/artifacts", nil))
	var response artifactStorageUsageResponse
	if err := json.Unmarshal(post.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertArtifactRoot(t, response, "current", 11, 1)

	writeUsageFile(t, name, 29)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/storage/usage/artifacts", nil))
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertArtifactRoot(t, response, "current", 11, 1)

	post = httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/storage/usage/artifacts", nil))
	if err := json.Unmarshal(post.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertArtifactRoot(t, response, "current", 29, 1)
}

func assertArtifactRoot(t *testing.T, response artifactStorageUsageResponse, id string, bytes int64, files int) artifactStorageUsageRoot {
	t.Helper()
	for _, root := range response.Roots {
		if root.ID == id {
			if root.Bytes != bytes || root.Files != files || root.Error != "" {
				t.Fatalf("root %s = %+v, want %d bytes and %d files", id, root, bytes, files)
			}
			return root
		}
	}
	t.Fatalf("missing root %q in %+v", id, response.Roots)
	return artifactStorageUsageRoot{}
}

func assertArtifactItem(t *testing.T, root artifactStorageUsageRoot, name string, bytes int64) artifactStorageUsageItem {
	t.Helper()
	for _, item := range root.Items {
		if item.Name == name {
			if item.Bytes != bytes || item.Error != "" {
				t.Fatalf("item %s = %+v, want %d bytes", name, item, bytes)
			}
			return item
		}
	}
	t.Fatalf("missing item %q in %+v", name, root.Items)
	return artifactStorageUsageItem{}
}

func assertArtifactFormat(t *testing.T, item artifactStorageUsageItem, extension string, bytes int64, files int) {
	t.Helper()
	for _, format := range item.Formats {
		if format.Extension == extension {
			if format.Bytes != bytes || format.Files != files {
				t.Fatalf("format %s = %+v, want %d bytes and %d files", extension, format, bytes, files)
			}
			return
		}
	}
	t.Fatalf("missing format %q in %+v", extension, item.Formats)
}
