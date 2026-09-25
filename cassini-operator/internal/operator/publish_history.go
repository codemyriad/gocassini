package operator

import (
	"context"
	"fmt"
)

// HasSuccessfulPublish distinguishes an interrupted first delivery from an
// intentional later audience edit. A republish must not recreate recipients an
// administrator removed after an earlier successful publish.
func (s *Store) HasSuccessfulPublish(ctx context.Context, jobID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts
WHERE job_id = ? AND stage = 'done' AND state = 'succeeded'`, jobID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("read publish history for %s: %w", jobID, err)
	}
	return count > 0, nil
}
