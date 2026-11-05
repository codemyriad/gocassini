package operator

import (
	"context"
	"encoding/json"
	"time"
)

// unsharedSummary is what is currently unshared across the archive, for the
// Recording sharing row: how many published meetings Nextcloud would not
// share with everyone, how many distinct people and groups are missing one,
// and since when — the oldest affected meeting's start. Nil when nothing is.
// Retired meetings are left out. Teams are counted in neither figure: the
// row says people and groups.
type unsharedSummary struct {
	Jobs   int    `json:"jobs"`
	Users  int    `json:"users"`
	Groups int    `json:"groups"`
	Since  string `json:"since"`
}

func (s *Store) unsharedSummary(ctx context.Context) (*unsharedSummary, error) {
	retired, err := s.retiredMeetingJobs(ctx)
	if err != nil {
		return nil, err
	}
	// Since is when the oldest affected meeting happened — its recording's
	// start, or an import's creation, the rule meetingFactsForJobs uses — not
	// when its shares were last reconciled: pressing Re-share on an old
	// meeting must not move "since" to today. A meeting whose start is not
	// known stands at its reconciliation time.
	rows, err := s.db.QueryContext(ctx, `
SELECT o.job_id, o.skipped, o.updated_at, COALESCE(j.record_started_at, ''),
  COALESCE((SELECT l.created_at_utc FROM meeting_lifecycle l
            WHERE l.name IN (o.job_id || '.opus', o.job_id || '.json')
            ORDER BY l.name LIMIT 1), '')
FROM job_share_outcomes o LEFT JOIN jobs j ON j.id = o.job_id
WHERE o.status = ?`, shareOutcomePartial)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users, groups := map[string]bool{}, map[string]bool{}
	var summary unsharedSummary
	var since time.Time
	for rows.Next() {
		var jobID, raw, updatedAt, recordStarted, created string
		if err := rows.Scan(&jobID, &raw, &updatedAt, &recordStarted, &created); err != nil {
			return nil, err
		}
		if retired[jobID] {
			continue
		}
		var skipped []skippedShare
		if err := json.Unmarshal([]byte(raw), &skipped); err != nil {
			continue
		}
		summary.Jobs++
		for _, skip := range skipped {
			switch skip.Type {
			case "user":
				users[skip.ID] = true
			case "group":
				groups[skip.ID] = true
			}
		}
		at := parseUTCTime(recordStarted)
		if at.IsZero() {
			at = parseUTCTime(created)
		}
		if at.IsZero() {
			at = parseUTCTime(updatedAt)
		}
		if !at.IsZero() && (since.IsZero() || at.Before(since)) {
			since = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if summary.Jobs == 0 {
		return nil, nil
	}
	summary.Users, summary.Groups = len(users), len(groups)
	if !since.IsZero() {
		summary.Since = since.UTC().Format(time.RFC3339)
	}
	return &summary, nil
}

// unsharedSummaryForRow reads what is unshared, for the Recording sharing
// row. A read that fails leaves the row without the count: the count
// explains the row, it never stops it being shown.
func (rt *Runtime) unsharedSummaryForRow(ctx context.Context) *unsharedSummary {
	if rt == nil || rt.store == nil {
		return nil
	}
	summary, err := rt.store.unsharedSummary(ctx)
	if err != nil {
		rt.logger.Printf("unshared summary: %v", err)
		return nil
	}
	return summary
}
