package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMeetingDeletionDisposalHandoffRecovery(t *testing.T) {
	for _, initial := range []string{"absent", "completed"} {
		for _, failure := range []string{"enqueue", "completion"} {
			t.Run(initial+"/"+failure, func(t *testing.T) {
				f := newMeetingDeleteFixture(t, ".opus")
				ctx := context.Background()
				insertJob(t, f.rt.store.db, "m", nowUTCString())
				if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded' WHERE id='m'`); err != nil {
					t.Fatal(err)
				}
				if initial == "completed" {
					if _, err := f.rt.store.db.Exec(`INSERT INTO media_cleanup(job_id,status) VALUES('m','completed')`); err != nil {
						t.Fatal(err)
					}
				}
				seal := filepath.Join(attemptSealDir(f.rt.cfg.WorkRoot, "m", 1), "m.json")
				mediaFile(t, seal)

				trigger := `CREATE TRIGGER fail_disposal_handoff BEFORE INSERT ON media_cleanup BEGIN SELECT RAISE(ABORT,'temporary enqueue failure'); END`
				if failure == "completion" {
					trigger = `CREATE TRIGGER fail_disposal_handoff BEFORE UPDATE ON remote_retention_operation WHEN NEW.status='completed' BEGIN SELECT RAISE(ABORT,'temporary completion failure'); END`
				}
				if _, err := f.rt.store.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
				err := f.service.prepareMeetingDeletion(ctx, "m.opus")
				var journalErr remoteRetentionJournalError
				if !errors.As(err, &journalErr) {
					t.Fatalf("handoff must report a durable journal failure, got %v", err)
				}
				if *f.exists || *f.deletes != 1 {
					t.Fatalf("remote deletion did not finish: exists=%v deletes=%d", *f.exists, *f.deletes)
				}
				pending, err := f.rt.store.pendingRemoteOperations(ctx)
				if err != nil || len(pending) != 1 || pending[0].InputETag != `"original"` {
					t.Fatalf("lost recoverable intent: pending=%+v err=%v", pending, err)
				}
				var count int
				if err := f.rt.store.db.QueryRow(`SELECT COUNT(*) FROM media_cleanup WHERE job_id='m'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if initial == "absent" {
					if count != 0 {
						t.Fatal("failed transaction left a cleanup row")
					}
				} else {
					var status string
					if err := f.rt.store.db.QueryRow(`SELECT status FROM media_cleanup WHERE job_id='m'`).Scan(&status); err != nil || status != "completed" {
						t.Fatalf("failed transaction changed prior cleanup: status=%s err=%v", status, err)
					}
				}
				if _, err := os.Stat(seal); err != nil {
					t.Fatalf("cleanup ran before handoff committed: %v", err)
				}
				if _, err := f.rt.store.db.Exec(`DROP TRIGGER fail_disposal_handoff`); err != nil {
					t.Fatal(err)
				}

				// A reader prevents the optional immediate nudge. Recovery must
				// still commit an obligation that the periodic worker can finish.
				func() {
					f.rt.store.artifactGate.RLock()
					defer f.rt.store.artifactGate.RUnlock()
					if err := f.service.recoverRemoteRetention(ctx); err != nil {
						t.Fatal(err)
					}
				}()
				var remoteStatus, cleanupStatus string
				var raw []byte
				if err := f.rt.store.db.QueryRow(`SELECT status,operation_json FROM remote_retention_operation WHERE name='m.opus'`).Scan(&remoteStatus, &raw); err != nil {
					t.Fatal(err)
				}
				var completed remoteRetentionOperation
				if err := json.Unmarshal(raw, &completed); err != nil {
					t.Fatal(err)
				}
				if err := f.rt.store.db.QueryRow(`SELECT status FROM media_cleanup WHERE job_id='m'`).Scan(&cleanupStatus); err != nil {
					t.Fatal(err)
				}
				if remoteStatus != "completed" || completed.InputETag != "" || cleanupStatus != "waiting" {
					t.Fatalf("handoff not committed: remote=%s etag=%q cleanup=%s", remoteStatus, completed.InputETag, cleanupStatus)
				}
				if _, err := os.Stat(seal); err != nil {
					t.Fatalf("reader did not defer disposal: %v", err)
				}
				f.rt.runMediaCleanupPass()
				assertAbsent(t, seal)
				if status := f.rt.mediaCleanupStatus(mustGetJob(t, f.rt.store, "m"), true); status.Status != "completed" {
					t.Fatalf("worker did not finish disposal: %+v", status)
				}
				if *f.deletes != 1 {
					t.Fatalf("recovery repeated remote DELETE: %d", *f.deletes)
				}
			})
		}
	}
}
