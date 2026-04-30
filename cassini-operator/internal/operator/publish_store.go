package operator

import (
	"context"
	"fmt"
	"strings"
)

func (s *Store) MarkPublishQueued(ctx context.Context, id, artifactMeetingPath, queuedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, publish_queued_at = ?, completed_at = NULL, error = NULL
WHERE id = ?`, "publish", "queued", artifactMeetingPath, queuedAt, queuedAt, queuedAt, id)
	if err != nil {
		return fmt.Errorf("update publish queued: %w", err)
	}
	return nil
}

func (s *Store) MarkPublishRunning(ctx context.Context, id, startedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, updated_at = ?, publish_started_at = ?
WHERE id = ?`, "publish", "running", startedAt, startedAt, id)
	if err != nil {
		return fmt.Errorf("update publish running: %w", err)
	}
	return nil
}

func (s *Store) MarkPublishSucceeded(ctx context.Context, id, artifactSitePath, finishedAt string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_site_path = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?, error = NULL
WHERE id = ?`, "done", "succeeded", artifactSitePath, finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update publish success: %w", err)
	}
	return nil
}

func (s *Store) MarkPublishFailed(ctx context.Context, id, artifactSitePath, errText, finishedAt string) error {
	artifactSitePath = strings.TrimSpace(artifactSitePath)
	if artifactSitePath == "" {
		_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
		if err != nil {
			return fmt.Errorf("update publish failure: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_site_path = ?, error = ?, updated_at = ?, publish_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", artifactSitePath, strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id)
	if err != nil {
		return fmt.Errorf("update publish failure: %w", err)
	}
	return nil
}
