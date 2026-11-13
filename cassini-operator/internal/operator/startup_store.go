package operator

import (
	"context"
	"database/sql"
	"fmt"
)

// MarkIncompleteJobsInterrupted stamps every job that was in flight when the
// operator died as interrupted at the current startup epoch. Jobs already in
// state 'interrupted' are excluded: their interrupted_at must keep the epoch
// of the crash that actually cut them short, because
// ListInterruptedTalkRecordJobs filters on the current sweep timestamp to
// notify each interruption exactly once — re-stamping would re-send spreed a
// failed callback for the same dead job on every restart (D-352/D-362).
// Resource-blocked builds are terminal until an administrator fixes the GPU
// runtime/capacity and explicitly reruns them, so restart recovery must leave
// their diagnostic state and ready run bundle intact.
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
WHERE state NOT IN (?, ?, ?, ?)
  AND NOT (state = 'queued' AND stage IN ('build', 'seal', 'publish'))`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed", "interrupted", "blocked")
	if err != nil {
		return 0, fmt.Errorf("mark incomplete jobs interrupted: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET state = ?, updated_at = ?, interrupted_at = ?
WHERE state NOT IN (?, ?, ?, ?)
  AND NOT (state = 'queued' AND stage IN ('build', 'seal', 'publish'))`, "interrupted", interruptedAt, interruptedAt, "succeeded", "failed", "interrupted", "blocked"); err != nil {
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

// InterruptedJobsAt lists the jobs one startup pass marked interrupted, so the
// runtime can emit recording.failed for each after the webhook configuration
// is loaded. The timestamp is unique to that pass.
func (s *Store) InterruptedJobsAt(ctx context.Context, at string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, current_attempt_number, stage, state, error
FROM jobs
WHERE interrupted_at = ?
ORDER BY id`, at)
	if err != nil {
		return nil, fmt.Errorf("list interrupted jobs: %w", err)
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var job Job
		var errorText sql.NullString
		if err := rows.Scan(&job.ID, &job.CurrentAttemptNumber, &job.Stage, &job.State, &errorText); err != nil {
			return nil, fmt.Errorf("scan interrupted job: %w", err)
		}
		if errorText.Valid {
			job.Error = &errorText.String
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted jobs: %w", err)
	}
	return jobs, nil
}
