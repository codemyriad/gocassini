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
// job id (the stem of its file). A meeting the operator does not know yields
// empty facts, never an error: a missing title is still a recording somebody
// is waiting for.
func (rt *Runtime) meetingFactsFor(ctx context.Context, jobID string) meetingFacts {
	return rt.meetingFactsForJobs(ctx, []string{jobID})[strings.TrimSpace(jobID)]
}

// meetingFactsForJobs reads the facts for several published meetings at
// once: the title and audio length from the metadata index, which is what
// Browse shows; the start from the job that recorded it; and, for a meeting
// no job recorded — an import — the start from its retention lifecycle. One
// query per record for the whole list, not four per meeting: /setup reads
// this for everything waiting for a person, inside a bounded time, and a
// hundred meetings must not cost hundreds of queries. Every id asked for
// has an entry, empty when the operator knows nothing about it.
func (rt *Runtime) meetingFactsForJobs(ctx context.Context, jobIDs []string) map[string]meetingFacts {
	facts := map[string]meetingFacts{}
	ids := make([]string, 0, len(jobIDs))
	for _, id := range jobIDs {
		id = strings.TrimSpace(id)
		if _, seen := facts[id]; id == "" || seen {
			continue
		}
		facts[id] = meetingFacts{}
		ids = append(ids, id)
	}
	if rt == nil || len(ids) == 0 {
		return facts
	}
	audioLength := map[string]time.Duration{}
	if rt.meetingMetadata != nil {
		entries, err := rt.meetingMetadata.entriesByName(ctx, meetingFileNames(ids))
		if err == nil {
			for _, id := range ids {
				for _, name := range meetingFileNames([]string{id}) {
					raw, ok := entries[name]
					if !ok {
						continue
					}
					var entry struct {
						Title            string `json:"title"`
						DigestDurationMS int64  `json:"digestDurationMs"`
					}
					if json.Unmarshal(raw, &entry) != nil {
						continue
					}
					f := facts[id]
					f.Title = oneLineTitle(entry.Title)
					facts[id] = f
					if entry.DigestDurationMS > 0 {
						audioLength[id] = time.Duration(entry.DigestDurationMS) * time.Millisecond
					}
					break
				}
			}
		}
	}
	if rt.store != nil {
		_ = forEachSQLBatch(ids, func(marks string, args []any) error {
			rows, err := rt.store.db.QueryContext(ctx, `SELECT id, record_started_at, record_finished_at FROM jobs WHERE id IN (`+marks+`)`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				var started, finished sql.NullString
				if err := rows.Scan(&id, &started, &finished); err != nil {
					return err
				}
				f := facts[id]
				f.StartedAt = parseUTCTime(started.String)
				if end := parseUTCTime(finished.String); !f.StartedAt.IsZero() && end.After(f.StartedAt) {
					f.Duration = end.Sub(f.StartedAt)
				}
				facts[id] = f
			}
			return rows.Err()
		})
		// Imports have no job: their lifecycle row says when they were
		// created. Of a meeting's two possible files the first by name is
		// the record, as meetingRetired reads it.
		unknown := []string{}
		for _, id := range ids {
			if facts[id].StartedAt.IsZero() {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			created := map[string]string{}
			_ = forEachSQLBatch(meetingFileNames(unknown), func(marks string, args []any) error {
				rows, err := rt.store.db.QueryContext(ctx, `SELECT name, created_at_utc FROM meeting_lifecycle WHERE name IN (`+marks+`) ORDER BY name`, args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var name, at string
					if err := rows.Scan(&name, &at); err != nil {
						return err
					}
					if _, have := created[meetingStem(name)]; !have {
						created[meetingStem(name)] = at
					}
				}
				return rows.Err()
			})
			for _, id := range unknown {
				if at := parseUTCTime(created[id]); !at.IsZero() {
					f := facts[id]
					f.StartedAt = at
					facts[id] = f
				}
			}
		}
	}
	// The audio as published is what the person will play; the recording's
	// own span only stands in when the index does not know the length.
	for id, length := range audioLength {
		f := facts[id]
		f.Duration = length
		facts[id] = f
	}
	return facts
}

// meetingFileNames is every file a meeting may be delivered as, audio first.
func meetingFileNames(jobIDs []string) []string {
	names := make([]string, 0, 2*len(jobIDs))
	for _, id := range jobIDs {
		names = append(names, id+".opus", id+".json")
	}
	return names
}

// sqlInBatch keeps an IN (...) list under what older SQLite builds allow in
// one statement, as EntriesFor does.
const sqlInBatch = 500

// forEachSQLBatch runs fn once per batch of values, with the placeholders
// and arguments for an IN (...) list of that batch.
func forEachSQLBatch(values []string, fn func(marks string, args []any) error) error {
	for start := 0; start < len(values); start += sqlInBatch {
		batch := values[start:min(start+sqlInBatch, len(values))]
		args := make([]any, len(batch))
		for i, value := range batch {
			args[i] = value
		}
		if err := fn(strings.TrimSuffix(strings.Repeat("?,", len(batch)), ","), args); err != nil {
			return err
		}
	}
	return nil
}

// parseUTCTime reads the operator's own timestamps (the fixed-width layout
// nowUTCString writes, or the RFC 3339 forms written before it), as
// parseInsightTime does; anything else is unknown, not an error.
func parseUTCTime(value string) time.Time {
	at, err := parseInsightTime(strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return at.UTC()
}
