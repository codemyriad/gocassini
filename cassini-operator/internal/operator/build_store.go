package operator

import (
	"context"
	"fmt"
	"strings"
)

func (s *Store) MarkBuildQueued(ctx context.Context, id, jobArtifactRunPath, attemptArtifactRunPath, queuedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build queued update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_run_path = ?, updated_at = ?, record_finished_at = ?, build_queued_at = ?, build_retry_not_before = NULL, completed_at = NULL, error = NULL
WHERE id = ?`, "build", "queued", jobArtifactRunPath, queuedAt, queuedAt, queuedAt, id)
	if err != nil {
		return fmt.Errorf("update build queued: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_run_path = ?, updated_at = ?, record_finished_at = ?, build_queued_at = ?, build_retry_not_before = NULL, completed_at = NULL, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "build", "queued", attemptArtifactRunPath, queuedAt, queuedAt, queuedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build queued: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build queued update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

// ClaimBuildRunning transitions a job from build/queued to build/running,
// returning false (without error) when the job is not claimable — e.g. a
// duplicate queue delivery after the requeue dispatcher re-scanned a row that
// another worker already picked up (D-367). The conditional UPDATE is the
// claim: SQLite serializes it, so exactly one worker wins.
func (s *Store) ClaimBuildRunning(ctx context.Context, task buildTask, startedAt string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin build running update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, build_started_at = ?, build_retry_not_before = NULL
WHERE id = ?
  AND current_attempt_number = ?
  AND artifact_run_path = ?
  AND stage = ? AND state = ?
  -- Parse instead of comparing TEXT so rows written by an older build using
  -- variable-width RFC3339Nano fractions remain chronologically correct.
  AND (build_retry_not_before IS NULL OR julianday(build_retry_not_before) <= julianday(?))`,
		"running", startedAt, startedAt,
		task.JobID, task.AttemptNumber, task.ArtifactRunPath,
		"build", "queued", startedAt)
	if err != nil {
		return false, fmt.Errorf("update build running: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("build running rows affected: %w", err)
	}
	if claimed == 0 {
		return false, nil
	}
	attemptResult, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, updated_at = ?, build_started_at = ?, build_retry_not_before = NULL
WHERE job_id = ? AND attempt_number = ? AND stage = ? AND state = ?`,
		"build", "running", startedAt, startedAt,
		task.JobID, task.AttemptNumber, "build", "queued")
	if err != nil {
		return false, fmt.Errorf("update attempt build running: %w", err)
	}
	attemptClaimed, err := attemptResult.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("attempt build running rows affected: %w", err)
	}
	if attemptClaimed != 1 {
		return false, fmt.Errorf("update attempt build running: expected 1 row, changed %d", attemptClaimed)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit build running update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", task.JobID, task.AttemptNumber)
	return true, nil
}

// MarkBuildDeferred atomically restores a transiently resource-constrained
// build from running to queued. It deliberately preserves build_queued_at and
// artifact paths so normal queue ordering and restart recovery remain intact.
func (s *Store) MarkBuildDeferred(ctx context.Context, id string, attemptNumber int, deferredAt, retryNotBefore string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin build deferred update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, build_started_at = NULL, build_retry_not_before = ?
WHERE id = ? AND current_attempt_number = ? AND stage = ? AND state = ?`,
		"queued", deferredAt, retryNotBefore, id, attemptNumber, "build", "running")
	if err != nil {
		return false, fmt.Errorf("update build deferred: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("build deferred rows affected: %w", err)
	}
	if changed == 0 {
		return false, nil
	}
	attemptResult, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET state = ?, updated_at = ?, build_started_at = NULL, build_retry_not_before = ?
WHERE job_id = ? AND attempt_number = ? AND stage = ? AND state = ?`,
		"queued", deferredAt, retryNotBefore, id, attemptNumber, "build", "running")
	if err != nil {
		return false, fmt.Errorf("update attempt build deferred: %w", err)
	}
	attemptChanged, err := attemptResult.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("attempt build deferred rows affected: %w", err)
	}
	if attemptChanged != 1 {
		return false, fmt.Errorf("update attempt build deferred: expected 1 row, changed %d", attemptChanged)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit build deferred update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return true, nil
}

// ListQueuedBuildTasks returns the durable build backlog: jobs whose row says
// build/queued with a canonical run bundle pointer. The requeue dispatcher
// feeds these to workers when the in-memory channel dropped them (full queue)
// or never saw them (operator restart) (D-367).
func (s *Store) ListQueuedBuildTasks(ctx context.Context) ([]buildTask, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, current_attempt_number, artifact_run_path
FROM jobs
WHERE stage = 'build' AND state = 'queued' AND artifact_run_path IS NOT NULL
  -- See ClaimBuildRunning: julianday also handles pre-fixed-width values.
  AND (build_retry_not_before IS NULL OR julianday(build_retry_not_before) <= julianday(?))
ORDER BY build_queued_at ASC, id ASC`, nowUTCString())
	if err != nil {
		return nil, fmt.Errorf("query queued build jobs: %w", err)
	}
	defer rows.Close()

	var tasks []buildTask
	for rows.Next() {
		var task buildTask
		if err := rows.Scan(&task.JobID, &task.AttemptNumber, &task.ArtifactRunPath); err != nil {
			return nil, fmt.Errorf("scan queued build job: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queued build jobs: %w", err)
	}
	return tasks, nil
}

func (s *Store) MarkBuildSucceeded(ctx context.Context, id, jobArtifactMeetingPath, attemptArtifactMeetingPath, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build success update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", jobArtifactMeetingPath, finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update build success: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, completed_at = ?, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "done", "succeeded", attemptArtifactMeetingPath, finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build success update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

func (s *Store) MarkBuildFailed(ctx context.Context, id, attemptArtifactMeetingPath, errText, finishedAt string) error {
	attemptArtifactMeetingPath = strings.TrimSpace(attemptArtifactMeetingPath)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id); err != nil {
		return fmt.Errorf("update build failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_meeting_path = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", nullableString(attemptArtifactMeetingPath), strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build failure update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}
