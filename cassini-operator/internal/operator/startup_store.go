package operator

import (
	"context"
	"fmt"
)

func (s *Store) MarkIncompleteJobsInterrupted(ctx context.Context, interruptedAt string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin interrupt update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?)`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed")
	if err != nil {
		return 0, fmt.Errorf("mark incomplete jobs interrupted: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?)`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed"); err != nil {
		return 0, fmt.Errorf("mark incomplete attempts interrupted: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit interrupt update: %w", err)
	}
	return count, nil
}
