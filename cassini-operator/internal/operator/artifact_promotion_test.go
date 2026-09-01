package operator

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func writePromotionFixtureDir(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(dir, name), err)
	}
}

func TestReconcilePromotionStagingRestoresBackupWhenDestinationMissing(t *testing.T) {
	workRoot := t.TempDir()
	stagingRoot := currentStagingRoot(workRoot)
	backupPath := filepath.Join(stagingRoot, "job1.meeting"+promotionBackupSuffix)
	writePromotionFixtureDir(t, backupPath, "cassini.json", "previous")

	actions, err := reconcilePromotionStaging(stagingRoot)
	if err != nil {
		t.Fatalf("reconcilePromotionStaging() error = %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one reconcile action, got %v", actions)
	}

	destination := canonicalMeetingPath(workRoot, "job1")
	raw, err := os.ReadFile(filepath.Join(destination, "cassini.json"))
	if err != nil {
		t.Fatalf("read restored destination: %v", err)
	}
	if string(raw) != "previous" {
		t.Fatalf("expected restored backup content, got %q", string(raw))
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("expected backup moved away, stat err = %v", err)
	}
}

func TestReconcilePromotionStagingDropsStaleBackupAndOrphans(t *testing.T) {
	workRoot := t.TempDir()
	stagingRoot := currentStagingRoot(workRoot)

	destination := canonicalMeetingPath(workRoot, "job1")
	writePromotionFixtureDir(t, destination, "cassini.json", "promoted")
	staleBackup := filepath.Join(stagingRoot, "job1.meeting"+promotionBackupSuffix)
	writePromotionFixtureDir(t, staleBackup, "cassini.json", "previous")
	orphan := filepath.Join(stagingRoot, "job2.run")
	writePromotionFixtureDir(t, orphan, "cassini.json", "half-copied")

	actions, err := reconcilePromotionStaging(stagingRoot)
	if err != nil {
		t.Fatalf("reconcilePromotionStaging() error = %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected two reconcile actions, got %v", actions)
	}

	raw, err := os.ReadFile(filepath.Join(destination, "cassini.json"))
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(raw) != "promoted" {
		t.Fatalf("expected destination untouched, got %q", string(raw))
	}
	if _, err := os.Stat(staleBackup); !os.IsNotExist(err) {
		t.Fatalf("expected stale backup removed, stat err = %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("expected orphaned staging copy removed, stat err = %v", err)
	}
}

func TestReconcilePromotionStagingMissingRootIsNoop(t *testing.T) {
	actions, err := reconcilePromotionStaging(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("reconcilePromotionStaging() error = %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("expected no actions, got %v", actions)
	}
}

func TestNewRuntimeReconcilesPromotionLeftoversAtStartup(t *testing.T) {
	tmp := t.TempDir()
	workRoot := filepath.Join(tmp, "jobs")
	siteRoot := filepath.Join(tmp, "site", "published")

	// Simulate a crash between replaceStagedDirectory's two renames for both
	// staging layouts: only the ".backup" copies survive, destinations gone.
	meetingBackup := filepath.Join(currentStagingRoot(workRoot), "job1.meeting"+promotionBackupSuffix)
	writePromotionFixtureDir(t, meetingBackup, "cassini.json", "previous-meeting")
	siteBackup := filepath.Join(siteStagingRoot(siteRoot), filepath.Base(siteRoot)+promotionBackupSuffix)
	writePromotionFixtureDir(t, siteBackup, "index.html", "previous-site")

	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	rt := NewRuntime(context.Background(), store, Config{
		DBPath:           filepath.Join(tmp, "jobs.sqlite3"),
		WorkRoot:         workRoot,
		SiteRoot:         siteRoot,
		MaxRecordWorkers: 1,
		MaxBuildWorkers:  1,
	}, log.New(io.Discard, "", 0), io.Discard, io.Discard)
	defer rt.Shutdown()

	raw, err := os.ReadFile(filepath.Join(canonicalMeetingPath(workRoot, "job1"), "cassini.json"))
	if err != nil {
		t.Fatalf("read restored meeting bundle: %v", err)
	}
	if string(raw) != "previous-meeting" {
		t.Fatalf("expected restored meeting bundle, got %q", string(raw))
	}
	raw, err = os.ReadFile(filepath.Join(siteRoot, "index.html"))
	if err != nil {
		t.Fatalf("read restored site: %v", err)
	}
	if string(raw) != "previous-site" {
		t.Fatalf("expected restored site, got %q", string(raw))
	}
}
