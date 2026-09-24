package operator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedRetentionJob(t *testing.T, rt *Runtime, id string) {
	t.Helper()
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	for _, table := range []string{"jobs", "job_attempts"} {
		col := "id"
		if table == "job_attempts" {
			col = "job_id"
		}
		if _, err := rt.store.db.Exec(`UPDATE `+table+` SET stage='done',state='failed',completed_at='2026-01-01T10:00:00Z' WHERE `+col+`=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "retention.json"))
	seedAttemptArtifacts(t, rt.cfg.WorkRoot, id, 1, false)
}
func TestRetentionHistoryAndLogs(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "history"
	seedRetentionJob(t, rt, id)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	rt.runRetentionSweep(context.Background(), now)
	assertExists(t, attemptMeetingPath(rt.cfg.WorkRoot, id, 1), "default forever")
	s := &rt.retention.settings
	s.History.Mode = "fine"
	s.History.Fine["failed_build"] = retentionPolicy{Count: 1, Unit: "days"}
	s.Logs = retentionPolicy{Count: 1, Unit: "weeks"}
	rt.runRetentionSweep(context.Background(), now)
	assertGone(t, attemptMeetingPath(rt.cfg.WorkRoot, id, 1), "failed build policy")
	assertGone(t, attemptSealDir(rt.cfg.WorkRoot, id, 1), "failed seal policy")
	assertGone(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "logs policy")
	assertExists(t, attemptRunPath(rt.cfg.WorkRoot, id, 1), "independent capture policy")
	assertExists(t, attemptSitePath(rt.cfg.WorkRoot, id, 1), "independent publication staging")
	if a, err := rt.store.ListJobAttempts(context.Background(), id); err != nil || len(a) != 1 {
		t.Fatal("metadata lost", err)
	}
	s.History.Mode = "group"
	s.History.Policy = retentionPolicy{Count: 1, Unit: "months"}
	rt.runRetentionSweep(context.Background(), now)
	assertGone(t, attemptRunPath(rt.cfg.WorkRoot, id, 1), "group policy")
	assertGone(t, attemptSitePath(rt.cfg.WorkRoot, id, 1), "group policy")
}
func TestRetentionSkipsReadersActiveJobsAndUnknownDates(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "active"
	seedRetentionJob(t, rt, id)
	rt.retention.settings.Logs = retentionPolicy{Count: 1, Unit: "days"}
	now := time.Now()
	unlock := rt.store.lockArtifacts(id)
	rt.runRetentionSweep(context.Background(), now)
	unlock()
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "reader reservation")
	_, err := rt.store.db.Exec(`UPDATE jobs SET stage='build',state='queued' WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	rt.runRetentionSweep(context.Background(), now)
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "queued")
	_, err = rt.store.db.Exec(`UPDATE jobs SET stage='done',state='failed' WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.store.db.Exec(`UPDATE job_attempts SET completed_at=NULL WHERE job_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	rt.runRetentionSweep(context.Background(), now)
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "unknown anchor")
}
func TestRetentionRefusesSymlinkTree(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "links"
	seedRetentionJob(t, rt, id)
	outside := filepath.Join(t.TempDir(), "important")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(attemptLogsDir(rt.cfg.WorkRoot, id, 1), "link")); err != nil {
		t.Fatal(err)
	}
	rt.retention.settings.Logs = retentionPolicy{Count: 1, Unit: "days"}
	rt.runRetentionSweep(context.Background(), time.Now())
	assertExists(t, outside, "not owned")
	assertExists(t, attemptLogsDir(rt.cfg.WorkRoot, id, 1), "unsafe tree rejected")
}

func TestRetentionSupersessionRequiresSuccessfulPublish(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "supersession"
	seedRetentionJob(t, rt, id)
	_, err := rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',publish_finished_at='2026-01-01T10:00:00Z' WHERE job_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.store.db.Exec(`INSERT INTO job_attempts(job_id,attempt_number,trigger_kind,request_json,stage,state,created_at,updated_at,completed_at,publish_finished_at) VALUES(?,2,'rerun','{}','done','failed','2026-02-01T00:00:00Z','2026-02-01T00:00:00Z','2026-02-01T00:00:00Z','2026-02-01T00:00:00Z')`, id)
	if err != nil {
		t.Fatal(err)
	}
	rt.retention.settings.History.Policy = retentionPolicy{Count: 1, Unit: "days"}
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rt.runRetentionSweep(context.Background(), now)
	assertExists(t, attemptSealDir(rt.cfg.WorkRoot, id, 1), "a failure is not supersession")
	_, err = rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded' WHERE job_id=? AND attempt_number=2`, id)
	if err != nil {
		t.Fatal(err)
	}
	rt.runRetentionSweep(context.Background(), now)
	assertGone(t, attemptSealDir(rt.cfg.WorkRoot, id, 1), "later successful publish")
}

func TestRetentionCanonicalArchivesIndependentAndRerunDenied(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "canonical"
	seedRetentionJob(t, rt, id)
	if err := os.RemoveAll(attemptRunPath(rt.cfg.WorkRoot, id, 1)); err != nil {
		t.Fatal(err)
	}
	source := seedReadyRunBundle(t, rt.cfg.WorkRoot, id)
	var promoteErr error
	source, promoteErr = promoteRunBundle(rt.cfg.WorkRoot, source, id)
	if promoteErr != nil {
		t.Fatal(promoteErr)
	}
	_, err := rt.store.db.Exec(`UPDATE jobs SET artifact_run_path=? WHERE id=?`, source, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.store.db.Exec(`UPDATE job_attempts SET record_finished_at='2026-01-01T10:00:00Z' WHERE job_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	meeting := canonicalMeetingPath(rt.cfg.WorkRoot, id)
	if err = os.MkdirAll(meeting, 0755); err != nil {
		t.Fatal(err)
	}
	opus := canonicalOpusPath(rt.cfg.WorkRoot, id)
	if err = os.WriteFile(opus, []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	seal := attemptOpusPath(rt.cfg.WorkRoot, id, 1)
	if err = os.Link(opus, seal); err != nil {
		t.Fatal(err)
	}
	_, err = rt.store.db.Exec(`INSERT INTO artifact_availability(job_id,published_attempt,output) VALUES(?,1,'present')`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',publish_finished_at='2026-02-01T00:00:00Z' WHERE job_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	s := &rt.retention.settings
	s.Recordings.Policy = retentionPolicy{Count: 1, Unit: "months"}
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rt.runRetentionSweep(context.Background(), now)
	assertGone(t, source, "source policy")
	assertGone(t, attemptRunPath(rt.cfg.WorkRoot, id, 1), "source duplicate cannot survive")
	assertExists(t, opus, "independent output policy")
	job := mustGetJob(t, rt.store, id)
	if _, err = rt.store.QueueRerunAttempt(context.Background(), job, nowUTCString()); err != ErrJobNotEligibleForRerun {
		t.Fatal("expired source accepted", err)
	}
	if got := rt.artifactAvailability(job); got.Source != "expired" || got.RerunBlockedReason == "" {
		t.Fatal(got)
	}
	s.Current = retentionPolicy{Count: 1, Unit: "weeks"}
	rt.runRetentionSweep(context.Background(), now)
	assertGone(t, meeting, "coupled archive")
	assertGone(t, opus, "coupled archive")
	assertGone(t, seal, "same-version hard link")
	if _, err = rt.store.GetJob(context.Background(), id); err != nil {
		t.Fatal("metadata removed", err)
	}
}
