package operator

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateLiveSiteManifestKeepsManifestOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory is not enforced for root")
	}
	siteDir := t.TempDir()
	manifestPath := filepath.Join(siteDir, "cassini.json")
	original := []byte(`{
  "kind": "site",
  "version": "cassini.site.v1",
  "source_path": "/tmp/meetings"
}
`)
	if err := os.WriteFile(manifestPath, original, 0o644); err != nil {
		t.Fatalf("write site manifest: %v", err)
	}

	// A read-only site directory makes the temp-file write fail before the
	// rename; the previous manifest must survive untouched.
	if err := os.Chmod(siteDir, 0o555); err != nil {
		t.Fatalf("chmod site dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(siteDir, 0o755) })

	err := UpdateLiveSiteManifest(siteDir, SiteBundleLineage{
		JobID:          "job1",
		AttemptNumber:  1,
		PublishedAtUTC: "2026-06-12T00:00:00Z",
	}, 1)
	if err == nil {
		t.Fatalf("expected lineage write failure in read-only dir")
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read site manifest after failed write: %v", err)
	}
	if !bytes.Equal(original, after) {
		t.Fatalf("expected site manifest unchanged after failed write, got %q", string(after))
	}
}

func TestUpdateLiveSiteManifestSeedsWhenAbsent(t *testing.T) {
	// A live site root has no manifest until something publishes into it — an
	// ExApp site root is created by the first delivery. Seeding rather than
	// erroring is what lets the first publish succeed.
	siteDir := t.TempDir()

	if err := UpdateLiveSiteManifest(siteDir, SiteBundleLineage{
		JobID:          "job1",
		AttemptNumber:  2,
		PublishedAtUTC: "2026-06-12T00:00:00Z",
	}, 3); err != nil {
		t.Fatalf("UpdateLiveSiteManifest() error = %v", err)
	}

	manifest, ok, err := LoadSiteBundleManifest(siteDir)
	if err != nil || !ok {
		t.Fatalf("LoadSiteBundleManifest() ok = %v err = %v", ok, err)
	}
	if manifest.Kind != "site" || manifest.Version != siteManifestVersion {
		t.Fatalf("seeded manifest = %#v, want a site manifest", manifest)
	}
	if manifest.MeetingCount != 3 {
		t.Fatalf("meeting_count = %d, want 3", manifest.MeetingCount)
	}
	if manifest.PublishedByJobID != "job1" || manifest.PublishedByAttemptNumber != 2 {
		t.Fatalf("lineage = %#v, want job1 attempt 2", manifest)
	}
	if manifest.State != bundleStateReady {
		t.Fatalf("state = %q, want %q", manifest.State, bundleStateReady)
	}
}
