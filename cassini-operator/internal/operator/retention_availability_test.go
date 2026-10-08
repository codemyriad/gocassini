package operator

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionEvictionVisibleInJobsList(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	insertJob(t, rt.store.db, "evicted", "2026-01-01T00:00:00Z")
	source := seedReadyRunBundle(t, rt.cfg.WorkRoot, "evicted")
	canonical, err := promoteRunBundle(rt.cfg.WorkRoot, source, "evicted")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.store.db.Exec(`UPDATE jobs SET artifact_run_path=? WHERE id='evicted'`, canonical); err != nil {
		t.Fatal(err)
	}
	insertJob(t, rt.store.db, "no-capture", "2026-01-01T00:00:00Z")
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
	anchor := time.Now().AddDate(0, 0, -2)
	if err = rt.expirePaths("evicted", 1, "recordings", retentionPolicy{Count: 1, Unit: "days"}, anchor, time.Now(), 0, canonical); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	rt.jobsHandler(w, httptest.NewRequest("GET", "/jobs", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var jobs []Job
	if err = json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatal(len(jobs))
	}
	for _, job := range jobs {
		if job.SourceExpired != (job.ID == "evicted") {
			t.Fatalf("wrong deletion state: %+v", job)
		}
		if job.ID == "evicted" && (job.ArtifactRunPath == nil || *job.ArtifactRunPath != canonical) {
			t.Fatal("provenance lost")
		}
	}
	job, err := rt.store.GetJob(context.Background(), "evicted")
	if err != nil {
		t.Fatal(err)
	}
	if rt.artifactAvailability(job).Source != "expired" {
		t.Fatal("list/detail availability disagree")
	}
}
