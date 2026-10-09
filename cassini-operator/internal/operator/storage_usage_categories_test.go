package operator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageCategoriesUseRetentionOwnershipAndDates(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	root := rt.cfg.WorkRoot
	seedRetentionJob(t, rt, "failed")
	// Replace the synthetic payload with known sizes. An identical extension in
	// different policy bundles must land in different categories.
	if err := os.RemoveAll(runsRoot(root)); err != nil {
		t.Fatal(err)
	}
	writeUsageFile(t, filepath.Join(attemptRunPath(root, "failed", 1), "audio.opus"), 11)
	writeUsageFile(t, filepath.Join(attemptMeetingPath(root, "failed", 1), "audio.opus"), 13)
	writeUsageFile(t, filepath.Join(attemptSealDir(root, "failed", 1), "audio.opus"), 17)
	writeUsageFile(t, filepath.Join(attemptSitePath(root, "failed", 1), "audio.opus"), 19)
	writeUsageFile(t, attemptLogPath(root, "failed", 1, "record"), 23)
	writeUsageFile(t, filepath.Join(runsRoot(root), "failed--attempt-001.run-extra", "audio.opus"), 29)

	seedRetentionJob(t, rt, "published")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET artifact_run_path=? WHERE id='published'`, canonicalRunPath(root, "published")); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',record_finished_at='2025-12-31T23:30:00-02:00',publish_finished_at='2026-01-03T12:00:00Z' WHERE job_id='published'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`INSERT INTO artifact_availability(job_id,published_attempt) VALUES('published',1)`); err != nil {
		t.Fatal(err)
	}
	writeUsageFile(t, filepath.Join(canonicalRunPath(root, "published"), "audio.opus"), 31)
	writeUsageFile(t, canonicalOpusPath(root, "published"), 37)
	writeUsageFile(t, filepath.Join(currentRoot(root), "published.json"), 41)

	index, err := rt.storageCategoryIndex(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failed := index.paths[attemptRunPath(root, "failed", 1)]
	if failed.category != "failed_capture" || failed.date != "2026-01-01" {
		t.Fatalf("failed capture = %+v", failed)
	}
	recorded := index.paths[canonicalRunPath(root, "published")]
	if recorded.category != "recordings" || recorded.date != "2026-01-01" {
		t.Fatalf("source = %+v", recorded)
	}
	if index.paths[attemptRunPath(root, "published", 1)] != recorded {
		t.Fatal("capture duplicate lost source policy")
	}
	current := index.paths[canonicalOpusPath(root, "published")]
	if index.paths[filepath.Join(currentRoot(root), "published.json")] != current {
		t.Fatal("JSON output lost current policy")
	}
	if current.category != "current" || current.date != "2026-01-03" {
		t.Fatalf("current = %+v", current)
	}
	if index.paths[attemptSealDir(root, "published", 1)] != current {
		t.Fatal("latest seal lost current policy")
	}
	result := ExAppConfig{}.scanDetailedStorageUsage(t.Context(), root, index.add)
	var directoryBytes, categoryBytes int64
	for _, d := range result.Directories {
		directoryBytes += d.Bytes
	}
	for _, c := range index.result() {
		categoryBytes += c.Bytes
		var dated int64
		for _, day := range c.Days {
			dated += day.Bytes
		}
		if dated+c.UndatedBytes != c.Bytes {
			t.Fatalf("category %s does not reconcile", c.ID)
		}
		if c.ID == "failed_build" && c.Bytes != 30 {
			t.Fatalf("failed build = %d", c.Bytes)
		}
		if c.ID == "other" && c.Bytes < 29 {
			t.Fatal("unmatched path omitted")
		}
	}
	if directoryBytes != categoryBytes {
		t.Fatalf("totals: categories=%d directories=%d", categoryBytes, directoryBytes)
	}
}

func TestStorageCategoriesUnknownDatesActiveAndSuperseded(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	seedRetentionJob(t, rt, "unknown")
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET completed_at=NULL WHERE job_id='unknown'`); err != nil {
		t.Fatal(err)
	}
	seedRetentionJob(t, rt, "active")
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET stage='build',state='running' WHERE job_id='active'`); err != nil {
		t.Fatal(err)
	}
	seedRetentionJob(t, rt, "rerun")
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',publish_finished_at='2026-01-02T00:00:00Z' WHERE job_id='rerun'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`INSERT INTO job_attempts(job_id,attempt_number,trigger_kind,request_json,stage,state,created_at,updated_at,publish_finished_at) VALUES('rerun',2,'manual','{}','done','succeeded','2026-02-02','2026-02-02','2026-02-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	index, err := rt.storageCategoryIndex(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	old := index.paths[attemptMeetingPath(rt.cfg.WorkRoot, "rerun", 1)]
	if old.category != "superseded" || old.date != "2026-02-02" {
		t.Fatalf("supersession date = %+v", old)
	}
	index.add(filepath.Join(attemptRunPath(rt.cfg.WorkRoot, "unknown", 1), "capture"), 10)
	index.add(filepath.Join(attemptRunPath(rt.cfg.WorkRoot, "active", 1), "capture"), 20)
	if c := index.categories["failed_capture"]; c.Bytes != 10 || c.UndatedBytes != 10 || c.UndatedFiles != 1 {
		t.Fatalf("undated = %+v", c)
	}
	if c := index.categories["other"]; c.Bytes != 20 {
		t.Fatalf("active = %+v", c)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := rt.storageCategoryIndex(ctx); err == nil {
		t.Fatal("cancellation ignored")
	}
}
