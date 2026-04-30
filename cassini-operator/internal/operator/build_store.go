package operator

import (
	"context"
	"fmt"
	"strings"
)

func (s *Store) MarkBuildQueued(ctx context.Context, id, artifactRunPath, queuedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_run_path = ?, updated_at = ?, record_finished_at = ?, build_queued_at = ?, completed_at = NULL, error = NULL
WHERE id = ?`, "build", "queued", artifactRunPath, queuedAt, queuedAt, queuedAt, id)
	if err != nil {
		return fmt.Errorf("update build queued: %w", err)
	}
	return nil
}

func (s *Store) MarkBuildRunning(ctx context.Context, id, startedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, updated_at = ?, build_started_at = ?
WHERE id = ?`, "build", "running", startedAt, startedAt, id)
	if err != nil {
		return fmt.Errorf("update build running: %w", err)
	}
	return nil
}

func (s *Store) MarkBuildSucceeded(ctx context.Context, id, artifactMeetingPath, finishedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", artifactMeetingPath, finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update build success: %w", err)
	}
	return nil
}

func (s *Store) MarkBuildFailed(ctx context.Context, id, artifactMeetingPath, errText, finishedAt string) error {
	artifactMeetingPath = strings.TrimSpace(artifactMeetingPath)
	if artifactMeetingPath == "" {
		_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
		if err != nil {
			return fmt.Errorf("update build failure: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, error = ?, updated_at = ?, build_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", artifactMeetingPath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update build failure: %w", err)
	}
	return nil
}
