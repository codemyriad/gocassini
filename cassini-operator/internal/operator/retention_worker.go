package operator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func nextRetentionSweep(now time.Time, schedule retentionSchedule) time.Time {
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return time.Time{}
	}
	clock, err := time.Parse("15:04", schedule.Time)
	if err != nil {
		return time.Time{}
	}
	local := now.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	wanted := clock.Hour()*60 + clock.Minute()
	// Search real instants rather than relying on time.Date's unspecified choice
	// at a DST transition. Use the first occurrence of a repeated time, or the
	// first available minute after a skipped time. At most one run per local date.
	for day := 0; day < 4; day++ {
		d := date.AddDate(0, 0, day)
		end := d.Add(48 * time.Hour)
		for instant := d.Add(-24 * time.Hour); instant.Before(end); instant = instant.Add(time.Minute) {
			wall := instant.In(loc)
			if wall.Year() != d.Year() || wall.Month() != d.Month() || wall.Day() != d.Day() || wall.Hour()*60+wall.Minute() < wanted {
				continue
			}
			if instant.After(now) {
				return instant
			}
			break
		}
	}
	return time.Time{}
}
func (rt *Runtime) startRetentionWorker() {
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		rt.runRetentionSweep(rt.ctx, time.Now())
		for {
			rt.retention.mu.Lock()
			schedule := rt.retention.settings.Schedule
			rt.retention.mu.Unlock()
			timer := time.NewTimer(time.Until(nextRetentionSweep(time.Now(), schedule)))
			select {
			case <-rt.ctx.Done():
				timer.Stop()
				return
			case <-rt.retention.changed:
				timer.Stop()
				continue
			case <-timer.C:
				rt.retention.mu.Lock()
				unchanged := rt.retention.settings.Schedule == schedule
				rt.retention.mu.Unlock()
				if !unchanged {
					continue
				}
				rt.runRetentionSweep(rt.ctx, time.Now())
			}
		}
	}()
}
func retentionAnchor(value *string) time.Time {
	if value == nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, *value)
	return t
}
func (rt *Runtime) runRetentionSweep(ctx context.Context, now time.Time) {
	if rt.retention == nil {
		return
	}
	// Read bounded pages; no SQLite transaction spans filesystem work.
	after := ""
	for {
		rows, err := rt.store.db.QueryContext(ctx, `SELECT id FROM jobs WHERE id > ? ORDER BY id LIMIT 100`, after)
		if err != nil {
			rt.logger.Printf("retention list failed: %v", err)
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
		rows.Close()
		if err != nil || len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			after = id
			if !validArtifactJob(id) || !rt.store.artifactGate.TryLock() {
				continue
			}
			unlock, owned := rt.store.tryLockArtifacts(id)
			if !owned {
				rt.store.artifactGate.Unlock()
				continue
			}
			rt.retention.mu.Lock()
			if rt.retention.loadErr != nil {
				rt.logger.Printf("retention disabled: %v", rt.retention.loadErr)
				rt.retention.mu.Unlock()
				unlock()
				rt.store.artifactGate.Unlock()
				return
			}
			settings := rt.retention.settings
			rt.retention.mu.Unlock()
			err = rt.expireJobArtifacts(ctx, id, settings, now)
			unlock()
			rt.store.artifactGate.Unlock()
			if err != nil {
				rt.logger.Printf("retention failed job=%s revision=%d: %v", id, settings.Revision, err)
			}
		}
	}
}

func (rt *Runtime) expireJobArtifacts(ctx context.Context, id string, s retentionSettings, now time.Time) error {
	job, err := rt.store.GetJob(ctx, id)
	if err != nil {
		return err
	}
	// Blocked/recoverable and queued work reserve all its local inputs.
	if job.Stage != "done" || (job.State != "succeeded" && job.State != "failed" && job.State != "interrupted") {
		return nil
	}
	attempts, err := rt.store.ListJobAttempts(ctx, id)
	if err != nil {
		return err
	}
	var replacement time.Time
	for _, a := range attempts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.Stage != "done" {
			continue
		}
		ended := retentionAnchor(a.CompletedAt)
		if ended.IsZero() {
			ended = retentionAnchor(a.InterruptedAt)
		}
		if err = rt.expirePaths(id, a.AttemptNumber, "logs", s.Logs, ended, now, s.Revision, attemptLogsDir(rt.cfg.WorkRoot, id, a.AttemptNumber)); err != nil {
			return err
		}
		if a.State == "succeeded" && a.PublishFinishedAt != nil {
			if !replacement.IsZero() {
				if err = rt.expirePaths(id, a.AttemptNumber, "superseded", s.History.policyFor("superseded"), replacement, now, s.Revision, attemptSealDir(rt.cfg.WorkRoot, id, a.AttemptNumber), attemptMeetingPath(rt.cfg.WorkRoot, id, a.AttemptNumber)); err != nil {
					return err
				}
			}
			replacement = retentionAnchor(a.PublishFinishedAt)
			continue
		}
		if a.State != "failed" && a.State != "interrupted" {
			continue
		}
		// A promoted capture belongs to recordings even if later processing fails.
		if job.ArtifactRunPath == nil || *job.ArtifactRunPath != canonicalRunPath(rt.cfg.WorkRoot, id) {
			if err = rt.expirePaths(id, a.AttemptNumber, "failed_capture", s.History.policyFor("failed_capture"), ended, now, s.Revision, attemptRunPath(rt.cfg.WorkRoot, id, a.AttemptNumber)); err != nil {
				return err
			}
		}
		if err = rt.expirePaths(id, a.AttemptNumber, "failed_build", s.History.policyFor("failed_build"), ended, now, s.Revision, attemptMeetingPath(rt.cfg.WorkRoot, id, a.AttemptNumber), attemptSealDir(rt.cfg.WorkRoot, id, a.AttemptNumber)); err != nil {
			return err
		}
		if err = rt.expirePaths(id, a.AttemptNumber, "failed_publish", s.History.policyFor("failed_publish"), ended, now, s.Revision, attemptSitePath(rt.cfg.WorkRoot, id, a.AttemptNumber)); err != nil {
			return err
		}
	}
	return rt.expireCanonicalArchives(ctx, job, attempts, s, now)
}

func (rt *Runtime) expireCanonicalArchives(ctx context.Context, job Job, attempts []JobAttempt, s retentionSettings, now time.Time) error {
	id := job.ID
	// Source age remains the original capture's age, not a rerun's date.
	if job.ArtifactRunPath != nil && *job.ArtifactRunPath == canonicalRunPath(rt.cfg.WorkRoot, id) {
		for _, a := range attempts {
			if a.RecordFinishedAt == nil {
				continue
			}
			if err := rt.expirePaths(id, a.AttemptNumber, "recordings", s.Recordings, retentionAnchor(a.RecordFinishedAt), now, s.Revision, canonicalRunPath(rt.cfg.WorkRoot, id), attemptRunPath(rt.cfg.WorkRoot, id, a.AttemptNumber)); err != nil {
				return err
			}

			break
		}
	}
	var published int
	if err := rt.store.db.QueryRow(`SELECT published_attempt FROM artifact_availability WHERE job_id=?`, id).Scan(&published); err != nil || published == 0 {
		return nil
	}
	for _, a := range attempts {
		if a.AttemptNumber != published {
			continue
		}
		if a.State != "succeeded" || a.PublishFinishedAt == nil {
			return nil
		}
		// Include the latest seal's hardlink/copy, but not different versions.
		return rt.expirePaths(id, published, "current", s.Current, retentionAnchor(a.PublishFinishedAt), now, s.Revision, canonicalMeetingPath(rt.cfg.WorkRoot, id), canonicalOpusPath(rt.cfg.WorkRoot, id), attemptSealDir(rt.cfg.WorkRoot, id, published), attemptMeetingPath(rt.cfg.WorkRoot, id, published))
	}
	return nil
}
func (rt *Runtime) expirePaths(id string, attempt int, kind string, p retentionPolicy, anchor, now time.Time, revision int, paths ...string) error {
	if p.Forever {
		return nil
	}
	if anchor.IsZero() {
		rt.logger.Printf("retention skipped job=%s attempt=%d kind=%s: unknown lifecycle date", id, attempt, kind)
		return nil
	}
	if !p.due(anchor, now) {
		return nil
	}
	op := artifactOperation{Job: id, Attempt: attempt, Action: "remove", Kind: kind, Revision: revision, Deadline: p.deadline(anchor).Format("2006-01-02")}
	for _, path := range paths {
		if err := validateArtifactTree(rt.cfg.WorkRoot, path); err != nil {
			return err
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		rel, err := filepath.Rel(rt.cfg.WorkRoot, path)
		if err != nil {
			return err
		}
		op.Targets = append(op.Targets, rel)
	}
	if len(op.Targets) == 0 {
		return nil
	}
	if rt.pendingArtifactOperation(id) {
		return fmt.Errorf("pending operation for job %s", id)
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
	return rt.startExpiryOperation(op)
}

// Staging/remuxing does not hold the settings mutex. Immediately before the
// destructive boundary, recheck the revision and linearize it with Save.
func (rt *Runtime) startExpiryOperation(op artifactOperation) error {
	if rt.retention != nil {
		rt.retention.mu.Lock()
		defer rt.retention.mu.Unlock()
		if rt.retention.loadErr != nil {
			return rt.retention.loadErr
		}
		if rt.retention.settings.Revision != op.Revision {
			return fmt.Errorf("settings changed before deletion; reconsider on next pass")
		}
	}
	if err := rt.saveOperation(op); err != nil {
		return err
	}
	return rt.finishOperation(op)
}
