package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type mediaCleanupStatus struct {
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

func terminalMediaJob(job Job) bool {
	return job.State == "interrupted" || (job.Stage == "done" && (job.State == "succeeded" || job.State == "failed"))
}

func (rt *Runtime) mediaCleanupStatus(job Job) *mediaCleanupStatus {
	if !deletesSourceMedia(job) {
		return nil
	}
	status := &mediaCleanupStatus{}
	err := rt.store.db.QueryRow(`SELECT status,last_error,completed_at FROM media_cleanup WHERE job_id=?`, job.ID).Scan(&status.Status, &status.LastError, &status.CompletedAt)
	if err != nil {
		status.Status = "error"
		status.LastError = "Cannot read media cleanup state"
	}
	if status.Status == "waiting" && terminalMediaJob(job) {
		status.Status = "pending"
	}
	return status
}

// Stage workers already own the job lock. Try the archive-reader gate rather
// than wait in the opposite lock order; the durable worker retries busy jobs.
func (rt *Runtime) cleanupMediaAfterStage(id string) {
	if !rt.store.artifactGate.TryLock() {
		return
	}
	defer rt.store.artifactGate.Unlock()
	rt.attemptMediaCleanup(id)
}

func (rt *Runtime) attemptMediaCleanup(id string) {
	job, err := rt.store.GetJob(context.Background(), id)
	if err != nil || !deletesSourceMedia(job) || !terminalMediaJob(job) {
		return
	}
	var status string
	var retryAt int64
	if err = rt.store.db.QueryRow(`SELECT status,retry_at FROM media_cleanup WHERE job_id=?`, id).Scan(&status, &retryAt); err != nil {
		rt.logger.Printf("media cleanup state failed job=%s: %v", id, err)
		return
	}
	if status == "completed" || retryAt > time.Now().Unix() {
		return
	}
	if err = rt.cleanupJobMedia(job); err != nil {
		var failures int
		_ = rt.store.db.QueryRow(`SELECT failures FROM media_cleanup WHERE job_id=?`, id).Scan(&failures)
		delay := time.Second * 30 * time.Duration(1<<min(failures, 5))
		if _, updateErr := rt.store.db.Exec(`UPDATE media_cleanup SET status='error',last_error=?,failures=failures+1,retry_at=? WHERE job_id=?`, err.Error(), time.Now().Add(delay).Unix(), id); updateErr != nil {
			rt.logger.Printf("media cleanup error persistence failed job=%s: %v", id, updateErr)
		}
		rt.logger.Printf("media cleanup failed job=%s: %v", id, err)
	}
	rt.store.emitStateChange(context.Background(), "job.updated", id, job.CurrentAttemptNumber)
}

// Caller reserves both the job and archive readers. The obligation remains
// pending until every journal has removed its old bytes, including hard links.
func (rt *Runtime) cleanupJobMedia(job Job) error {
	if !validArtifactJob(job.ID) || !deletesSourceMedia(job) || !terminalMediaJob(job) {
		return errors.New("job is not eligible for media cleanup")
	}
	if _, err := rt.store.db.Exec(`UPDATE media_cleanup SET status='pending' WHERE job_id=?`, job.ID); err != nil {
		return err
	}
	var raw string
	err := rt.store.db.QueryRow(`SELECT operation FROM artifact_operations WHERE job_id=?`, job.ID).Scan(&raw)
	if err == nil {
		op, e := decodeArtifactOperation(raw)
		if e != nil {
			return e
		}
		if e = rt.finishOperation(op); e != nil {
			return e
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), job.ID)
	if err != nil {
		return err
	}
	for _, a := range attempts {
		// Keep the immutable JSON seal (including on publication failure). Everything
		// else in the seal directory is temporary output, including partial Opus packs.
		paths := []string{canonicalRunPath(rt.cfg.WorkRoot, job.ID), canonicalMeetingPath(rt.cfg.WorkRoot, job.ID), canonicalOpusPath(rt.cfg.WorkRoot, job.ID), attemptRunPath(rt.cfg.WorkRoot, job.ID, a.AttemptNumber), attemptMeetingPath(rt.cfg.WorkRoot, job.ID, a.AttemptNumber), attemptSitePath(rt.cfg.WorkRoot, job.ID, a.AttemptNumber), attemptScratchPath(rt.cfg.WorkRoot, job.ID, a.AttemptNumber)}
		paths = append(paths, mediaPromotionPaths(rt.cfg.WorkRoot, job.ID)...)
		seal := attemptSealDir(rt.cfg.WorkRoot, job.ID, a.AttemptNumber)
		if err := validateArtifactTree(rt.cfg.WorkRoot, seal); err != nil {
			return err
		}
		entries, e := os.ReadDir(seal)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		for _, entry := range entries {
			if entry.Name() != job.ID+".json" {
				paths = append(paths, filepath.Join(seal, entry.Name()))
			}
		}
		if err := rt.removeMediaPaths(job.ID, a.AttemptNumber, paths); err != nil {
			return err
		}
	}
	tx, err := rt.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO artifact_availability(job_id,source) VALUES(?,'deleted') ON CONFLICT(job_id) DO UPDATE SET source='deleted'`, job.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE jobs SET artifact_meeting_path=NULL WHERE id=?`, job.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE media_cleanup SET status='completed',last_error='',completed_at=?,retry_at=0 WHERE job_id=?`, nowUTCString(), job.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func mediaPromotionPaths(root, id string) []string {
	var paths []string
	for _, ext := range []string{".run", ".meeting", ".opus"} {
		for _, suffix := range []string{"", promotionBackupSuffix} {
			paths = append(paths, filepath.Join(currentStagingRoot(root), id+ext+suffix))
		}
	}
	return paths
}

func (rt *Runtime) removeMediaPaths(id string, attempt int, paths []string) error {
	op := artifactOperation{Job: id, Attempt: attempt, Action: "remove", Kind: "media-disposal"}
	for _, p := range paths {
		if err := validateArtifactTree(rt.cfg.WorkRoot, p); err != nil {
			return err
		}
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		rel, err := filepath.Rel(rt.cfg.WorkRoot, p)
		if err != nil {
			return err
		}
		op.Targets = append(op.Targets, rel)
	}
	if len(op.Targets) == 0 {
		return nil
	}
	if rt.pendingArtifactOperation(id) {
		return errors.New("pending artifact operation")
	}
	dir := rt.operationDir(id)
	if err := validateArtifactTree(rt.cfg.WorkRoot, dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := rt.saveOperation(op); err != nil {
		return err
	}
	return rt.finishOperation(op)
}

func (rt *Runtime) startMediaCleanupWorker() {
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		rt.runMediaCleanupPass()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
				rt.runMediaCleanupPass()
			}
		}
	}()
}

func (rt *Runtime) runMediaCleanupPass() {
	// Only admitted disposal obligations, not an orphan scan or age-policy sweep.
	rows, err := rt.store.db.QueryContext(rt.ctx, `SELECT c.job_id FROM media_cleanup c JOIN jobs j ON j.id=c.job_id WHERE c.status!='completed' AND c.retry_at<=? AND (j.state='interrupted' OR (j.stage='done' AND j.state IN ('succeeded','failed'))) ORDER BY c.retry_at,c.job_id LIMIT 100`, time.Now().Unix())
	if err != nil {
		if rt.ctx.Err() == nil {
			rt.logger.Printf("media cleanup scan failed: %v", err)
		}
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		rt.logger.Printf("media cleanup scan failed: %v", err)
		return
	}
	for _, id := range ids {
		if rt.ctx.Err() != nil {
			return
		}
		if !rt.store.artifactGate.TryLock() {
			continue
		}
		unlock, ok := rt.store.tryLockArtifacts(id)
		if ok {
			rt.attemptMediaCleanup(id)
			unlock()
		}
		rt.store.artifactGate.Unlock()
	}
}

func (rt *Runtime) handleRetryMediaCleanup(w http.ResponseWriter, r *http.Request, id string) {
	job, err := rt.store.GetJob(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, 404, "job not found")
		return
	}
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	if !deletesSourceMedia(job) || !terminalMediaJob(job) {
		writeJSONError(w, 409, "job is not eligible for media cleanup")
		return
	}
	if _, err = rt.store.db.ExecContext(r.Context(), `UPDATE media_cleanup SET retry_at=0 WHERE job_id=?`, id); err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	// Wake-up is bounded by the worker interval, never an untracked goroutine.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func attemptScratchPath(root, id string, attempt int) string {
	return filepath.Join(runsRoot(root), attemptBaseName(id, attempt)+".scratch")
}

func (rt *Runtime) mediaScratchEnv(env []string, id string, attempt int) ([]string, error) {
	job, err := rt.store.GetJob(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if !deletesSourceMedia(job) {
		return env, nil
	}
	path, err := filepath.Abs(attemptScratchPath(rt.cfg.WorkRoot, id, attempt))
	if err != nil {
		return nil, err
	}
	if err = validateArtifactTree(rt.cfg.WorkRoot, path); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	return setEnvKey(env, "TMPDIR", path), nil
}

func requireCompletedTranscription(meeting string) error {
	raw, err := os.ReadFile(filepath.Join(meeting, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest struct {
		Processing struct {
			Transcription struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"transcription"`
		} `json:"processing"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if manifest.Processing.Transcription.Status != "completed" {
		return fmt.Errorf("transcription did not complete (%s: %s); source media will be deleted", manifest.Processing.Transcription.Status, manifest.Processing.Transcription.Reason)
	}
	return nil
}
