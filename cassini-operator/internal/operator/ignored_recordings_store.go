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

// SetRecordingsIgnored adds or removes ids from the ignore set.
//
// Idempotent in both directions: ignoring an already-ignored recording and
// un-ignoring one that was never ignored both succeed and change nothing, so a
// double-click costs a statement rather than an error. `ignoredAt` records when
// the decision was taken; it is not read by anything today and is there because
// a set of bare ids with no dates is the kind of table nobody can later explain.
func (s *Store) SetRecordingsIgnored(ctx context.Context, ids []string, ignored bool, ignoredAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ignored recordings update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range ids {
		if id == "" {
			continue
		}
		if ignored {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO ignored_recordings (meeting_id, ignored_at) VALUES (?, ?)
ON CONFLICT(meeting_id) DO NOTHING`, id, ignoredAt); err != nil {
				return fmt.Errorf("ignore recording %s: %w", id, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ignored_recordings WHERE meeting_id = ?`, id); err != nil {
			return fmt.Errorf("un-ignore recording %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ignored recordings update: %w", err)
	}
	return nil
}

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
