package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// meetingFacts is what the operator itself knows about a published meeting
// when it tells a person about it: the title Browse shows, when it started
// and how long it runs.
//
// Every time here is UTC. The operator records a recording's start and end
// in UTC (jobs.record_started_at and record_finished_at), and an imported
// meeting's createdAtUtc is UTC by name. Neither records a zone, so none is
// applied: a share note says "10:00 UTC" for a meeting that started then.
type meetingFacts struct {
	Title     string
	StartedAt time.Time     // zero when unknown
	Duration  time.Duration // zero when unknown
}

// meetingFactsFor reads the facts for one published meeting, named by its
// job id (the stem of its file): the title and audio length from the
// metadata index, which is what Browse shows; the start from the job that
// recorded it; and, for a meeting no job recorded — an import — the start
// from its retention lifecycle. A meeting the operator does not know yields
// empty facts, never an error: a missing title is still a recording somebody
// is waiting for.
func (rt *Runtime) meetingFactsFor(ctx context.Context, jobID string) meetingFacts {
	var facts meetingFacts
	jobID = strings.TrimSpace(jobID)
	if rt == nil || jobID == "" {
		return facts
	}
	var audioLength time.Duration
	if rt.meetingMetadata != nil {
		for _, name := range []string{jobID + ".opus", jobID + ".json"} {
			fileID, err := rt.meetingMetadata.FileIDFor(ctx, name)
			if err != nil || fileID <= 0 {
				continue
			}
			entries, err := rt.meetingMetadata.EntriesFor(ctx, []int64{fileID})
			if err != nil {
				continue
			}
			var entry struct {
				Title            string `json:"title"`
				DigestDurationMS int64  `json:"digestDurationMs"`
			}
			if json.Unmarshal(entries[fileID], &entry) != nil {
				continue
			}
			facts.Title = oneLineTitle(entry.Title)
			if entry.DigestDurationMS > 0 {
				audioLength = time.Duration(entry.DigestDurationMS) * time.Millisecond
			}
			break
		}
	}
	if rt.store != nil {
		var started, finished sql.NullString
		err := rt.store.db.QueryRowContext(ctx, `SELECT record_started_at, record_finished_at FROM jobs WHERE id = ?`, jobID).Scan(&started, &finished)
		if err == nil {
			facts.StartedAt = parseUTCTime(started.String)
			if end := parseUTCTime(finished.String); !facts.StartedAt.IsZero() && end.After(facts.StartedAt) {
				facts.Duration = end.Sub(facts.StartedAt)
			}
		}
		if facts.StartedAt.IsZero() {
			var created string
			err := rt.store.db.QueryRowContext(ctx, `SELECT created_at_utc FROM meeting_lifecycle WHERE name IN (?, ?) ORDER BY name LIMIT 1`, jobID+".opus", jobID+".json").Scan(&created)
			if err == nil {
				facts.StartedAt = parseUTCTime(created)
			}
		}
	}
	// The audio as published is what the person will play; the recording's
	// own span only stands in when the index does not know the length.
	if audioLength > 0 {
		facts.Duration = audioLength
	}
	return facts
}

// parseUTCTime reads the operator's own timestamps (RFC 3339, nanoseconds or
// not). Anything else is unknown, not an error.
func parseUTCTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UTC()
		}
	}
	return time.Time{}
}
