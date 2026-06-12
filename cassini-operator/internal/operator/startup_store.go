package operator

import (
	"context"
	"fmt"
)

// MarkIncompleteJobsInterrupted stamps every job that was in flight when the
// operator died as interrupted at the current startup epoch. Jobs already in
// state 'interrupted' are excluded: their interrupted_at must keep the epoch
// of the crash that actually cut them short, because
// ListInterruptedTalkRecordJobs filters on the current sweep timestamp to
// notify each interruption exactly once — re-stamping would re-send spreed a
// failed callback for the same dead job on every restart (D-352/D-362).
// QueueRerunAttempt clears interrupted_at when a job is rerun, so nothing
// depends on refreshing the stamp.
func (s *Store) MarkIncompleteJobsInterrupted(ctx context.Context, interruptedAt string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin interrupt update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?, ?)`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed", "interrupted")
	if err != nil {
		return 0, fmt.Errorf("mark incomplete jobs interrupted: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?, ?)`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed", "interrupted"); err != nil {
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
