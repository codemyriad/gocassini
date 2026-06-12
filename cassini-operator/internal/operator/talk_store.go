package operator

import (
	"context"
	"fmt"
)

// SetJobTalkBinding persists the Talk room binding for a job started through
// the Talk recording backend (D-352). The binding is written once at start
// time and never cleared: it documents which room the recording belongs to
// for startup cleanup and rerun re-delivery.
func (s *Store) SetJobTalkBinding(ctx context.Context, id, bindingJSON string) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET talk_binding = ?, updated_at = ?
WHERE id = ?`, bindingJSON, nowUTCString(), id); err != nil {
		return fmt.Errorf("update talk binding: %w", err)
	}
	return nil
}

// MarkTalkDelivered records that the recording reached Nextcloud (stopped
// callback acknowledged and upload stored). Reruns skip re-delivery once it
// is set.
func (s *Store) MarkTalkDelivered(ctx context.Context, id, deliveredAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin talk delivered update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET talk_delivered_at = ?, updated_at = ?
WHERE id = ?`, deliveredAt, deliveredAt, id); err != nil {
		return fmt.Errorf("update talk delivered: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit talk delivered update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

// interruptedTalkJob is a Talk-bound job whose recording was cut short by an
// operator restart and whose delivery never completed.
type interruptedTalkJob struct {
	ID      string
	Binding string
}

// ListInterruptedTalkRecordJobs returns the Talk-bound record-stage jobs
// marked interrupted by the startup sweep at interruptedAt. Filtering on the
// sweep timestamp keeps older interrupted history from re-notifying rooms
// that may have started a fresh recording since.
func (s *Store) ListInterruptedTalkRecordJobs(ctx context.Context, interruptedAt string) ([]interruptedTalkJob, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, talk_binding
FROM jobs
WHERE stage = 'record'
  AND state = 'interrupted'
  AND interrupted_at = ?
  AND talk_binding IS NOT NULL
  AND talk_delivered_at IS NULL
ORDER BY created_at ASC, id ASC`, interruptedAt)
	if err != nil {
		return nil, fmt.Errorf("query interrupted talk jobs: %w", err)
	}
	defer rows.Close()

	var jobs []interruptedTalkJob
	for rows.Next() {
		var job interruptedTalkJob
		if err := rows.Scan(&job.ID, &job.Binding); err != nil {
			return nil, fmt.Errorf("scan interrupted talk job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted talk jobs: %w", err)
	}
	return jobs, nil
}
