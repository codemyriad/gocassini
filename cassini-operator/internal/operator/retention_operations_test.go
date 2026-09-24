package operator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
