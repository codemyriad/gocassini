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
//
// Queued build/seal/publish rows are also excluded: their inputs (the canonical
// run/meeting bundles and the sealed `.opus`) are durable on disk and the
// requeue dispatcher re-delivers them after a restart, so marking them
// interrupted would strand resumable work (D-367). Only record-stage jobs (whose
// recorder process died with the operator) and running build/seal/publish jobs
// (whose subprocess died) are truly interrupted.
//
// `seal` joins that list rather than sitting outside it because sealing is what
// makes a job publishable (D-583): a seal/queued row stranded as interrupted
// would be a recording that silently never publishes, which is exactly the
// failure mode the seal stage exists to remove.
func (s *Store) MarkIncompleteJobsInterrupted(ctx context.Context, interruptedAt string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin interrupt update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?, ?)
  AND NOT (state = 'queued' AND stage IN ('build', 'seal', 'publish'))`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed", "interrupted")
	if err != nil {
		return 0, fmt.Errorf("mark incomplete jobs interrupted: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?, ?)
  AND NOT (state = 'queued' AND stage IN ('build', 'seal', 'publish'))`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed", "interrupted"); err != nil {
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
