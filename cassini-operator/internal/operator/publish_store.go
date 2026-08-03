package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MarkPublishQueued moves a job to publish/queued.
//
// Production never calls it: publish/queued is reachable only through
// MarkSealSucceeded, which writes it in the same transaction that records the
// sealed artifact, so a queued publish always has an artifact behind it
// (D-583). It survives for tests that need to stand a publish/queued row up
// without running a seal.
func (s *Store) MarkPublishQueued(ctx context.Context, id, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish queued update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, publish_queued_at = ?, completed_at = NULL, error = NULL
WHERE id = ?`, "publish", "queued", jobArtifactMeetingPath, queuedAt, queuedAt, queuedAt, id)
	if err != nil {
		return fmt.Errorf("update publish queued: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, publish_queued_at = ?, completed_at = NULL, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "publish", "queued", attemptArtifactMeetingPath, queuedAt, queuedAt, queuedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt publish queued: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish queued update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

// errPublishNotClaimable marks a publish task whose jobs row is no longer
// publish/queued — a duplicate queue delivery (direct enqueue plus a
// requeue-dispatcher re-scan) or a state change since queueing. The publish
// worker treats it as a skip, never a re-run (D-367).
var errPublishNotClaimable = errors.New("job is not publish/queued (already claimed by another delivery or state changed)")

// MarkPublishRunning claims a job for the publish worker: only a
// publish/queued row transitions to running. The conditional UPDATE is the
// claim — SQLite serializes it, so duplicate deliveries surface as
// errPublishNotClaimable instead of running publish twice (D-367).
func (s *Store) MarkPublishRunning(ctx context.Context, id, startedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish running update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, publish_started_at = ?
WHERE id = ? AND stage = ? AND state = ?`, "running", startedAt, startedAt, id, "publish", "queued")
	if err != nil {
		return fmt.Errorf("update publish running: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("publish running rows affected: %w", err)
	}
	if claimed == 0 {
		return errPublishNotClaimable
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, updated_at = ?, publish_started_at = ?
WHERE job_id = ? AND attempt_number = ?`, "publish", "running", startedAt, startedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt publish running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish running update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

// ListQueuedPublishTasks returns the durable publish backlog (publish/queued
// rows) for the requeue dispatcher: tasks the channel dropped (full queue) or
// never saw (operator restart) (D-367).
func (s *Store) ListQueuedPublishTasks(ctx context.Context) ([]publishTask, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, current_attempt_number
FROM jobs
WHERE stage = 'publish' AND state = 'queued'
ORDER BY publish_queued_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query queued publish jobs: %w", err)
	}
	defer rows.Close()

	var tasks []publishTask
	for rows.Next() {
		var task publishTask
		if err := rows.Scan(&task.JobID, &task.AttemptNumber); err != nil {
			return nil, fmt.Errorf("scan queued publish job: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queued publish jobs: %w", err)
	}
	return tasks, nil
}

func (s *Store) MarkPublishSucceeded(ctx context.Context, id, jobArtifactSitePath, attemptArtifactSitePath, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish success update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_site_path = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", jobArtifactSitePath, finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update publish success: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_site_path = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "done", "succeeded", attemptArtifactSitePath, finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt publish success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish success update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

func (s *Store) MarkPublishFailed(ctx context.Context, id, jobArtifactSitePath, attemptArtifactSitePath, errText, finishedAt string) error {
	jobArtifactSitePath = strings.TrimSpace(jobArtifactSitePath)
	attemptArtifactSitePath = strings.TrimSpace(attemptArtifactSitePath)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if jobArtifactSitePath == "" {
		_, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
		if err != nil {
			return fmt.Errorf("update publish failure: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_site_path = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", nullableString(attemptArtifactSitePath), strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
			return fmt.Errorf("update attempt publish failure: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit publish failure update: %w", err)
		}
		s.emitStateChange(ctx, "job.updated", id, attemptNumber)
		return nil
	}
	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_site_path = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", jobArtifactSitePath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update publish failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_site_path = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", nullableString(attemptArtifactSitePath), strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt publish failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish failure update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}
