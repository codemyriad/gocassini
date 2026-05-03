package operator

import (
	"context"
	"fmt"
	"strings"
)

func (s *Store) MarkBuildQueued(ctx context.Context, id, artifactRunPath, queuedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build queued update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_run_path = ?, updated_at = ?, record_finished_at = ?, build_queued_at = ?, completed_at = NULL, error = NULL
WHERE id = ?`, "build", "queued", artifactRunPath, queuedAt, queuedAt, queuedAt, id)
	if err != nil {
		return fmt.Errorf("update build queued: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_run_path = ?, updated_at = ?, record_finished_at = ?, build_queued_at = ?, completed_at = NULL, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "build", "queued", artifactRunPath, queuedAt, queuedAt, queuedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build queued: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build queued update: %w", err)
	}
	return nil
}

func (s *Store) MarkBuildRunning(ctx context.Context, id, startedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build running update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, updated_at = ?, build_started_at = ?
WHERE id = ?`, "build", "running", startedAt, startedAt, id)
	if err != nil {
		return fmt.Errorf("update build running: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, updated_at = ?, build_started_at = ?
WHERE job_id = ? AND attempt_number = ?`, "build", "running", startedAt, startedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build running update: %w", err)
	}
	return nil
}

func (s *Store) MarkBuildSucceeded(ctx context.Context, id, artifactMeetingPath, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build success update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", artifactMeetingPath, finishedAt, finishedAt, finishedAt, id)
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
WHERE job_id = ? AND attempt_number = ?`, "done", "succeeded", artifactMeetingPath, finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build success update: %w", err)
	}
	return nil
}

func (s *Store) MarkBuildFailed(ctx context.Context, id, artifactMeetingPath, errText, finishedAt string) error {
	artifactMeetingPath = strings.TrimSpace(artifactMeetingPath)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin build failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if artifactMeetingPath == "" {
		_, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
		if err != nil {
			return fmt.Errorf("update build failure: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
			return fmt.Errorf("update attempt build failure: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit build failure update: %w", err)
		}
		return nil
	}
	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", artifactMeetingPath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update build failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_meeting_path = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", artifactMeetingPath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt build failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit build failure update: %w", err)
	}
	return nil
}
