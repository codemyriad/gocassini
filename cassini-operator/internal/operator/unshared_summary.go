package operator

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// unsharedSummary is what is currently unshared across the archive, for the
// Recording sharing row: how many published meetings Nextcloud would not
// share with everyone, how many distinct people and groups are missing one,
// and since when — the oldest affected meeting's start. Nil when nothing is.
// Retired meetings are left out. Teams are counted in neither figure: the
// row says people and groups. Recipients Nextcloud's limit on new shares held
// back are not counted: Cassini is still sharing with them (shareLimitedSummary).
type unsharedSummary struct {
	Jobs   int    `json:"jobs"`
	Users  int    `json:"users"`
	Groups int    `json:"groups"`
	Since  string `json:"since"`
}

// shareLimitedSummary is, for the same Recording sharing row and over the same
// meetings the unshared count reads (every one not retired), how many still
// have recipients held back by Nextcloud's limit on new shares — 20 every 10
// minutes per account — waiting for a re-share. Since is the oldest such
// meeting's start, the rule unsharedSummary uses. Nil when none is waiting.
type shareLimitedSummary struct {
	Jobs  int    `json:"jobs"`
	Since string `json:"since"`
}

// shareOutcomeRow is one recorded outcome with the meeting time "since" is
// read from.
type shareOutcomeRow struct {
	jobID   string
	status  string
	entries []skippedShare
	at      time.Time
}

// shareOutcomeRows reads the outcomes with one of these statuses, retired
// meetings left out. At is when the meeting happened — its recording's
// start, or an import's creation, the rule meetingFactsForJobs uses — not
// when its shares were last reconciled: pressing Re-share on an old meeting
// must not move "since" to today. A meeting whose start is not known stands
// at its reconciliation time.
func (s *Store) shareOutcomeRows(ctx context.Context, statuses ...string) ([]shareOutcomeRow, error) {
	retired, err := s.retiredMeetingJobs(ctx)
	if err != nil {
		return nil, err
	}
	query := `
SELECT o.job_id, o.status, o.skipped, o.updated_at, COALESCE(j.record_started_at, ''),
  COALESCE((SELECT l.created_at_utc FROM meeting_lifecycle l
            WHERE l.name IN (o.job_id || '.opus', o.job_id || '.json')
            ORDER BY l.name LIMIT 1), '')
FROM job_share_outcomes o LEFT JOIN jobs j ON j.id = o.job_id
WHERE o.status IN (?` + strings.Repeat(",?", len(statuses)-1) + `)
ORDER BY o.job_id`
	args := make([]any, len(statuses))
	for i, status := range statuses {
		args[i] = status
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shareOutcomeRow
	for rows.Next() {
		var row shareOutcomeRow
		var raw, updatedAt, recordStarted, created string
		if err := rows.Scan(&row.jobID, &row.status, &raw, &updatedAt, &recordStarted, &created); err != nil {
			return nil, err
		}
		if retired[row.jobID] {
			continue
		}
		if err := json.Unmarshal([]byte(raw), &row.entries); err != nil {
			continue
		}
		row.at = parseUTCTime(recordStarted)
		if row.at.IsZero() {
			row.at = parseUTCTime(created)
		}
		if row.at.IsZero() {
			row.at = parseUTCTime(updatedAt)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func earliest(since, at time.Time) time.Time {
	if !at.IsZero() && (since.IsZero() || at.Before(since)) {
		return at
	}
	return since
}

func formatSince(since time.Time) string {
	if since.IsZero() {
		return ""
	}
	return since.UTC().Format(time.RFC3339)
}

func (s *Store) unsharedSummary(ctx context.Context) (*unsharedSummary, error) {
	rows, err := s.shareOutcomeRows(ctx, shareOutcomePartial)
	if err != nil {
		return nil, err
	}
	users, groups := map[string]bool{}, map[string]bool{}
	var summary unsharedSummary
	var since time.Time
	for _, row := range rows {
		refused := refusedShares(row.entries)
		if len(refused) == 0 {
			continue
		}
		summary.Jobs++
		for _, skip := range refused {
			switch skip.Type {
			case "user":
				users[skip.ID] = true
			case "group":
				groups[skip.ID] = true
			}
		}
		since = earliest(since, row.at)
	}
	if summary.Jobs == 0 {
		return nil, nil
	}
	summary.Users, summary.Groups = len(users), len(groups)
	summary.Since = formatSince(since)
	return &summary, nil
}

// shareLimitedSummary reads it from the outcomes that can still hold a
// deferred recipient: a partial or a deferred one.
func (s *Store) shareLimitedSummary(ctx context.Context) (*shareLimitedSummary, error) {
	rows, err := s.shareOutcomeRows(ctx, shareOutcomePartial, shareOutcomeDeferred)
	if err != nil {
		return nil, err
	}
	var summary shareLimitedSummary
	var since time.Time
	for _, row := range rows {
		if len(deferredShares(row.entries)) == 0 {
			continue
		}
		summary.Jobs++
		since = earliest(since, row.at)
	}
	if summary.Jobs == 0 {
		return nil, nil
	}
	summary.Since = formatSince(since)
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

// shareLimitedSummaryForRow is shareLimitedSummary for the Recording sharing
// row, as best-effort as the unshared count.
func (rt *Runtime) shareLimitedSummaryForRow(ctx context.Context) *shareLimitedSummary {
	if rt == nil || rt.store == nil {
		return nil
	}
	summary, err := rt.store.shareLimitedSummary(ctx)
	if err != nil {
		rt.logger.Printf("share limit summary: %v", err)
		return nil
	}
	return summary
}
