package operator

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSiteBundleLineageKeepsManifestOnWriteFailure(t *testing.T) {
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

	err := WriteSiteBundleLineage(siteDir, SiteBundleLineage{
		JobID:          "job1",
		AttemptNumber:  1,
		PublishedAtUTC: "2026-06-12T00:00:00Z",
	})
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
