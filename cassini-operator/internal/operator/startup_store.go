package operator

import (
	"context"
	"fmt"
)

func (s *Store) MarkIncompleteJobsInterrupted(ctx context.Context, interruptedAt string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?)`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed")
	if err != nil {
		return 0, fmt.Errorf("mark incomplete jobs interrupted: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return count, nil
}
