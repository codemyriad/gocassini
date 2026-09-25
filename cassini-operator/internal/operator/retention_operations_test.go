package operator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedPairPreservesPreviousUntilSuccess(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "pair"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	meeting := attemptMeetingPath(rt.cfg.WorkRoot, id, 1)
	if err := os.MkdirAll(meeting, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meeting, "manifest"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	opus, err := writeSealedOpusFixture(rt)(context.Background(), sealTask{JobID: id, AttemptNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := fileSHA256(opus)
	if _, err = rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',stage='done',artifact_opus_sha256=?,publish_finished_at='2026-01-01T01:00:00Z' WHERE job_id=?`, digest, id); err != nil {
		t.Fatal(err)
	}
	if err = rt.promotePublishedPair(id, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := fileSHA256(canonicalOpusPath(rt.cfg.WorkRoot, id))
	if got != digest {
		t.Fatal("wrong seal")
	}
	if err = rt.promotePublishedPair(id, 2); err == nil {
		t.Fatal("unpublished attempt promoted")
	}
	got, _ = fileSHA256(canonicalOpusPath(rt.cfg.WorkRoot, id))
	if got != digest {
		t.Fatal("lost published seal")
	}
	if rt.pendingArtifactOperation(id) {
		t.Fatal("journal not cleared")
	}
}

func TestPublishedPairRecoversEveryRenameBoundary(t *testing.T) {
	for phase := 0; phase <= 4; phase++ {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			id := "phases"
			insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
			dir := rt.operationDir(id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			op := artifactOperation{Job: id, Attempt: 1, Action: "promote", Digest: "new"}
			for i, p := range []string{canonicalMeetingPath(rt.cfg.WorkRoot, id), canonicalOpusPath(rt.cfg.WorkRoot, id)} {
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("new-%d", i)), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
				rel, _ := filepath.Rel(rt.cfg.WorkRoot, p)
				op.Targets = append(op.Targets, rel)
			}
			if err := rt.saveOperation(op); err != nil {
				t.Fatal(err)
			}
			for step := 0; step < phase; step++ {
				i := step / 2
				p := filepath.Join(rt.cfg.WorkRoot, op.Targets[i])
				var err error
				if step%2 == 0 {
					err = os.Rename(p, filepath.Join(dir, fmt.Sprintf("old-%d", i)))
				} else {
					err = os.Rename(filepath.Join(dir, fmt.Sprintf("new-%d", i)), p)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			rt.recoverArtifactOperations()
			for _, rel := range op.Targets {
				b, err := os.ReadFile(filepath.Join(rt.cfg.WorkRoot, rel))
				if err != nil || string(b) != "new" {
					t.Fatalf("mixed generation phase=%d: %s %v", phase, b, err)
				}
			}
			if rt.pendingArtifactOperation(id) {
				t.Fatal("not recovered")
			}
		})
	}
}

func TestPublishedPairRecoversLegacySealWithoutInventingMeeting(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "legacy"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	opus, err := writeSealedOpusFixture(rt)(context.Background(), sealTask{JobID: id, AttemptNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := fileSHA256(opus)
	if _, err = rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',publish_finished_at='2026-01-01T01:00:00Z',artifact_opus_sha256=? WHERE job_id=?`, digest, id); err != nil {
		t.Fatal(err)
	}
	if err = rt.promotePublishedPair(id, 1); err != nil {
		t.Fatal(err)
	}
	assertGone(t, canonicalMeetingPath(rt.cfg.WorkRoot, id), "lost intermediate must not be fabricated")
	assertExists(t, canonicalOpusPath(rt.cfg.WorkRoot, id), "retained published seal")
	job := mustGetJob(t, rt.store, id)
	if job.ArtifactMeetingPath != nil {
		t.Fatal("missing intermediate advertised")
	}
}

func TestCaptureDuplicateCleanupDoesNotWaitForPublish(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "capture"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	attempt := seedReadyRunBundle(t, rt.cfg.WorkRoot, id)
	canonical, err := promoteRunBundle(rt.cfg.WorkRoot, attempt, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.store.db.Exec(`UPDATE jobs SET stage='build',state='queued',artifact_run_path=? WHERE id=?`, canonical, id); err != nil {
		t.Fatal(err)
	}
	if _, err = rt.store.db.Exec(`UPDATE job_attempts SET artifact_run_path=?,record_finished_at='2026-01-01T01:00:00Z' WHERE job_id=?`, attempt, id); err != nil {
		t.Fatal(err)
	}
	rt.removeSuccessfulCaptureDuplicate(id, 1)
	assertGone(t, attempt, "successful capture duplicate")
	assertExists(t, canonical, "ready source before publication")
}

func TestTimedRetentionDoesNotRunDuplicateCleanup(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "scheduled-cleanup"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	attempt := seedReadyRunBundle(t, rt.cfg.WorkRoot, id)
	canonical, err := promoteRunBundle(rt.cfg.WorkRoot, attempt, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.store.db.Exec(`UPDATE jobs SET stage='done',state='failed',artifact_run_path=? WHERE id=?`, canonical, id); err != nil {
		t.Fatal(err)
	}
	if _, err = rt.store.db.Exec(`UPDATE job_attempts SET stage='done',state='failed',artifact_run_path=?,record_finished_at='2026-01-01T01:00:00Z' WHERE job_id=?`, attempt, id); err != nil {
		t.Fatal(err)
	}
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "retention.json"))

	rt.runRetentionSweep(context.Background(), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	assertExists(t, attempt, "timed retention must not perform duplicate cleanup")

	rt.reconcileArtifactDuplicatesOnStartup()
	assertGone(t, attempt, "startup reconciliation finishes duplicate cleanup")
}

func TestArtifactOperationRecoveryAndBoundaries(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "recover"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	p := attemptMeetingPath(rt.cfg.WorkRoot, id, 1)
	if err := os.MkdirAll(p, 0755); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(rt.cfg.WorkRoot, p)
	op := artifactOperation{Job: id, Attempt: 1, Action: "remove", Targets: []string{rel}}
	if err := os.MkdirAll(rt.operationDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := rt.saveOperation(op); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption immediately after the rename.
	if err := os.Rename(p, filepath.Join(rt.operationDir(id), "old-0")); err != nil {
		t.Fatal(err)
	}
	rt.recoverArtifactOperations()
	assertGone(t, p, "interrupted removal finished")
	if rt.pendingArtifactOperation(id) {
		t.Fatal("pending")
	}
	op.Targets = []string{"../outside"}
	if rt.finishOperation(op) == nil {
		t.Fatal("escape accepted")
	}
	if err := os.Symlink(t.TempDir(), p); err != nil {
		t.Fatal(err)
	}
	op.Targets = []string{rel}
	if rt.finishOperation(op) == nil {
		t.Fatal("symlink accepted")
	}
}
