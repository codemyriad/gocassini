package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// summaryNotice is where a meeting's summary was sent (#532).
type summaryNotice struct {
	Host   string `json:"host"`
	Reason string `json:"reason,omitempty"`
}

// projectSummaryNotices adds summarySentTo to the catalog entries whose
// job's summary a receiver accepted. An entry's id is its job's id, so an
// imported meeting gets none. Added when served, like room names: the stored
// entries are not rewritten.
func (s *Store) projectSummaryNotices(ctx context.Context, entries []json.RawMessage) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, summary_sent_host, COALESCE(summary_sent_reason, '') FROM jobs WHERE COALESCE(summary_sent_host, '') <> ''`)
	if err != nil {
		return nil, fmt.Errorf("read summary notices: %w", err)
	}
	notices := map[string]summaryNotice{}
	for rows.Next() {
		var id string
		var notice summaryNotice
		if err := rows.Scan(&id, &notice.Host, &notice.Reason); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read summary notices: %w", err)
		}
		notices[id] = notice
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(notices) == 0 {
		return entries, nil
	}
	out := make([]json.RawMessage, len(entries))
	for i, raw := range entries {
		out[i] = raw
		var fields map[string]json.RawMessage
		var id string
		if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["id"], &id) != nil {
			continue
		}
		notice, ok := notices[id]
		if !ok {
			continue
		}
		fields["summarySentTo"], _ = json.Marshal(notice)
		if projected, err := json.Marshal(fields); err == nil {
			out[i] = projected
		}
	}
	return out, nil
}

// pendingSummaryNotice is what the Talk post says a meeting's summary will be
// sent to: the newest summary.completed row for jobID queued for receiverKey
// (the receiver in effect) that is waiting or delivered. ok is false when
// there is none, or no receiver ("").
func (s *Store) pendingSummaryNotice(ctx context.Context, jobID, receiverKey string) (summaryNotice, bool, error) {
	if receiverKey == "" {
		return summaryNotice{}, false, nil
	}
	var notice summaryNotice
	err := s.db.QueryRowContext(ctx, `SELECT notice_host, COALESCE(notice_reason, '') FROM event_outbox
WHERE job_id = ? AND type = ? AND receiver_id = ? AND failed_at IS NULL AND COALESCE(notice_host, '') <> ''
ORDER BY created_at DESC LIMIT 1`, jobID, eventSummaryCompleted, receiverKey).Scan(&notice.Host, &notice.Reason)
	if err == sql.ErrNoRows {
		return summaryNotice{}, false, nil
	}
	if err != nil {
		return summaryNotice{}, false, fmt.Errorf("read pending summary notice: %w", err)
	}
	return notice, true, nil
}
