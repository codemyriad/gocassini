package operator

import (
	"context"
	"fmt"
)

// The ignore set (D-769): the one durable decision behind the open-recordings
// list, and deliberately the only one.
//
// Everything else the list renders is derived from the archive and the jobs
// table on every read, so it cannot go stale. This cannot be derived from
// anything — it is an administrator saying "I know, leave it" about a recording
// that is genuinely still readable by everyone, either because nothing can ever
// narrow it or because they opened it on purpose.

// ListIgnoredRecordings returns the ignored meeting ids as a set.
//
// A set rather than a slice because every caller asks "is this one ignored"
// while walking the archive, and the archive is the bigger of the two.
func (s *Store) ListIgnoredRecordings(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT meeting_id FROM ignored_recordings`)
	if err != nil {
		return nil, fmt.Errorf("query ignored recordings: %w", err)
	}
	defer rows.Close()

	ignored := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan ignored recording: %w", err)
		}
		ignored[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ignored recordings: %w", err)
	}
	return ignored, nil
}
