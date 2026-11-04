package operator

import (
	"context"
	"encoding/json"
	"time"
)

// unsharedSummary is what is currently unshared across the archive, for the
// Recording sharing row: how many published meetings Nextcloud would not
// share with everyone, how many distinct people and groups are missing one,
// and since when. Nil when nothing is. Retired meetings are left out. Teams
// are counted in neither figure: the row says people and groups.
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
	rows, err := s.db.QueryContext(ctx, `SELECT job_id, skipped, updated_at FROM job_share_outcomes WHERE status = ?`, shareOutcomePartial)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users, groups := map[string]bool{}, map[string]bool{}
	var summary unsharedSummary
	var since time.Time
	for rows.Next() {
		var jobID, raw, updatedAt string
		if err := rows.Scan(&jobID, &raw, &updatedAt); err != nil {
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
		if at := parseUTCTime(updatedAt); !at.IsZero() && (since.IsZero() || at.Before(since)) {
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
