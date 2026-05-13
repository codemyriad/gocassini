package operator

import (
	"context"
	"fmt"
	"strings"
)

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

func (s *Store) MarkPublishRunning(ctx context.Context, id, startedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish running update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, updated_at = ?, publish_started_at = ?
WHERE id = ?`, "publish", "running", startedAt, startedAt, id)
	if err != nil {
		return fmt.Errorf("update publish running: %w", err)
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

func (s *Store) MarkPublishSucceeded(ctx context.Context, id, artifactSitePath, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish success update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_site_path = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", artifactSitePath, finishedAt, finishedAt, finishedAt, id)
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
WHERE job_id = ? AND attempt_number = ?`, "done", "succeeded", artifactSitePath, finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt publish success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish success update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

func (s *Store) MarkPublishFailed(ctx context.Context, id, artifactSitePath, errText, finishedAt string) error {
	artifactSitePath = strings.TrimSpace(artifactSitePath)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publish failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if artifactSitePath == "" {
		_, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
		if err != nil {
			return fmt.Errorf("update publish failure: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
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
WHERE id = ?`, "done", "failed", artifactSitePath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update publish failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_site_path = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", artifactSitePath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt publish failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish failure update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}
