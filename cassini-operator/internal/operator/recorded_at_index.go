package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Past ExApp recordings were published before recordedAtUtc existed, and the
// meeting index (meetings.sqlite3) only changes on publish, import or reseed,
// so nothing would ever give their entries an instant. This file stamps one
// into each recorded meeting's indexed entry, derived from what the operator
// still holds about the recording (chooseRecordedAt), and records that the
// delivered file needs the same value (recorded_at_files.go, state needed —
// rewriting it waits for an administrator). A meeting with no jobs row —
// an import, a seeded legacy entry — is left exactly as it is.

// recordedAtIndexReport is one pass over the index.
type recordedAtIndexReport struct {
	// Stamped counts entries given an instant, by the source it came from.
	Stamped map[recordedAtSource]int
	// Present counts entries that already carried one (done earlier, or a
	// new publish whose file states it).
	Present int
	// NoJob counts entries the operator did not record: left untouched.
	NoJob int
	// NoEvidence counts recorded entries for which no source gave an instant.
	NoEvidence int
	// Raced counts entries replaced by a publish while this pass ran; the
	// replacement is stamped by whoever wrote it, or by the next pass, which
	// startRecordedAtIndexBackfill runs when this count is not zero.
	Raced int
}

func (r recordedAtIndexReport) String() string {
	parts := []string{}
	for _, source := range recordedAtSources {
		parts = append(parts, fmt.Sprintf("%s=%d", source, r.Stamped[source]))
	}
	return fmt.Sprintf("stamped{%s} present=%d no-job=%d no-evidence=%d raced=%d",
		strings.Join(parts, " "), r.Present, r.NoJob, r.NoEvidence, r.Raced)
}

type recordedAtIndexRow struct {
	fileID   int64
	opusName string
	entry    string
	column   string
}

// backfillRecordedAtIndex makes one pass. It is idempotent — an entry that
// carries recordedAtUtc is done — and resumable, since every entry commits
// on its own. It never holds a lock a catalog read waits on.
func (rt *Runtime) backfillRecordedAtIndex(ctx context.Context) (recordedAtIndexReport, error) {
	report := recordedAtIndexReport{Stamped: map[recordedAtSource]int{}}
	if rt == nil || rt.meetingMetadata == nil || rt.store == nil {
		return report, nil
	}
	rows, err := rt.meetingMetadata.db.QueryContext(ctx, `SELECT file_id, opus_name, entry_json, recorded_at_utc FROM meeting_metadata ORDER BY file_id`)
	if err != nil {
		return report, err
	}
	var all []recordedAtIndexRow
	for rows.Next() {
		var row recordedAtIndexRow
		if err := rows.Scan(&row.fileID, &row.opusName, &row.entry, &row.column); err != nil {
			rows.Close()
			return report, err
		}
		all = append(all, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for _, row := range all {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if rt.recordedAtIndexBeforeStamp != nil {
			rt.recordedAtIndexBeforeStamp(row)
		}
		outcome, source, err := rt.stampRecordedAtRow(ctx, row, recordedAtStateNeeded)
		if err != nil {
			return report, fmt.Errorf("stamp %s: %w", row.opusName, err)
		}
		switch outcome {
		case recordedAtStamped:
			report.Stamped[source]++
		case recordedAtPresent:
			report.Present++
		case recordedAtNoJob:
			report.NoJob++
		case recordedAtNoEvidence:
			report.NoEvidence++
		case recordedAtRaced:
			report.Raced++
		}
	}
	return report, nil
}

type recordedAtStampOutcome int

const (
	recordedAtStamped recordedAtStampOutcome = iota
	recordedAtPresent
	recordedAtNoJob
	recordedAtNoEvidence
	recordedAtRaced
)

// stampRecordedAtRow stamps one indexed entry when the operator recorded it
// and it has no instant yet, and enters its delivered file in the file
// backfill as fileState (needed, or pending where the administrator already
// asked for it).
func (rt *Runtime) stampRecordedAtRow(ctx context.Context, row recordedAtIndexRow, fileState string) (recordedAtStampOutcome, recordedAtSource, error) {
	if present := entryRecordedAtUTC(json.RawMessage(row.entry)); present != "" {
		// The entry states its instant; the column only mirrors it. A file
		// that says so needs no rewrite, so nothing is queued.
		if normalized := normalizedRecordedAtUTC(present); row.column != normalized {
			if _, err := rt.meetingMetadata.db.ExecContext(ctx, `UPDATE meeting_metadata SET recorded_at_utc=? WHERE file_id=? AND entry_json=?`, normalized, row.fileID, row.entry); err != nil {
				return 0, "", err
			}
		}
		return recordedAtPresent, "", nil
	}
	jobID := meetingStem(row.opusName)
	var started sql.NullString
	err := rt.store.db.QueryRowContext(ctx, `SELECT record_started_at FROM jobs WHERE id = ?`, jobID).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) {
		return recordedAtNoJob, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	at, source, ok := chooseRecordedAt(rt.recordedAtEvidenceFor(ctx, jobID, started.String))
	if !ok {
		return recordedAtNoEvidence, "", nil
	}
	value := formatRecordedAtUTC(at)
	stamped, err := withEntryRecordedAtUTC(json.RawMessage(row.entry), value)
	if err != nil {
		// Not an object: nothing a reader could carry the field in.
		return recordedAtNoEvidence, "", nil
	}
	tx, err := rt.meetingMetadata.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()
	// Guarded on the entry read: a publish that replaced it meanwhile wins.
	result, err := tx.ExecContext(ctx, `UPDATE meeting_metadata SET entry_json=?, recorded_at_utc=? WHERE file_id=? AND entry_json=?`, string(stamped), value, row.fileID, row.entry)
	if err != nil {
		return 0, "", err
	}
	if n, err := result.RowsAffected(); err != nil {
		return 0, "", err
	} else if n == 0 {
		return recordedAtRaced, "", nil
	}
	if meetingExtension(row.opusName) == ".opus" {
		if err := queueRecordedAtFileTx(ctx, tx, row.opusName, value, fileState); err != nil {
			return 0, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return recordedAtStamped, source, nil
}

// queueRecordedAtFileTx records that the delivered file should carry value,
// as state (needed or pending). A file already entered keeps its progress.
func queueRecordedAtFileTx(ctx context.Context, tx *sql.Tx, opusName, value, state string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO recorded_at_file_backfill(opus_name, recorded_at_utc, state, updated_at) VALUES(?, ?, ?, ?) ON CONFLICT(opus_name) DO NOTHING`, opusName, value, state, nowUTCString())
	return err
}

// stampPublishedRecordedAt runs right after a publish indexed an entry. An
// entry that states its instant is left alone; otherwise the meeting is
// stamped as the backfill would, and its file — just replaced — entered
// afresh: pending when the administrator had already asked for the old file
// (any state past needed), needed otherwise. A republish neither starts the
// migration for a file nobody asked about nor drops one somebody did.
func (rt *Runtime) stampPublishedRecordedAt(ctx context.Context, fileID int64, opusName string) error {
	if rt == nil || rt.meetingMetadata == nil || rt.store == nil {
		return nil
	}
	var row recordedAtIndexRow
	err := rt.meetingMetadata.db.QueryRowContext(ctx, `SELECT file_id, opus_name, entry_json, recorded_at_utc FROM meeting_metadata WHERE file_id=?`, fileID).Scan(&row.fileID, &row.opusName, &row.entry, &row.column)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.opusName != opusName) {
		return nil
	}
	if err != nil {
		return err
	}
	// The delivery replaced the file, so whatever an earlier pass did to the
	// old one says nothing about it: either the new file states its instant
	// (nothing to do) or it is queued afresh below.
	fileState := recordedAtStateNeeded
	var previous string
	err = rt.meetingMetadata.db.QueryRowContext(ctx, `SELECT state FROM recorded_at_file_backfill WHERE opus_name=?`, opusName).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous != "" && previous != recordedAtStateNeeded {
		fileState = recordedAtStatePending
	}
	if _, err := rt.meetingMetadata.db.ExecContext(ctx, `DELETE FROM recorded_at_file_backfill WHERE opus_name=?`, opusName); err != nil {
		return err
	}
	if _, _, err := rt.stampRecordedAtRow(ctx, row, fileState); err != nil {
		return err
	}
	rt.wakeRecordedAtFiles()
	return nil
}

// adoptFileRecordedAt records what a delivered file itself states when it
// disagrees with the index: the manifest is the record.
func (rt *Runtime) adoptFileRecordedAt(ctx context.Context, opusName, value string) error {
	normalized := normalizedRecordedAtUTC(value)
	if normalized == "" {
		return fmt.Errorf("file states an invalid recordedAtUtc %q", value)
	}
	rows, err := rt.meetingMetadata.db.QueryContext(ctx, `SELECT file_id, entry_json FROM meeting_metadata WHERE opus_name=?`, opusName)
	if err != nil {
		return err
	}
	type pair struct {
		id    int64
		entry string
	}
	var found []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.entry); err != nil {
			rows.Close()
			return err
		}
		found = append(found, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range found {
		stamped, err := withEntryRecordedAtUTC(json.RawMessage(p.entry), strings.TrimSpace(value))
		if err != nil {
			continue
		}
		if _, err := rt.meetingMetadata.db.ExecContext(ctx, `UPDATE meeting_metadata SET entry_json=?, recorded_at_utc=? WHERE file_id=? AND entry_json=?`, string(stamped), normalized, p.id, p.entry); err != nil {
			return err
		}
	}
	return nil
}

// A pass that lost entries to a concurrent write runs again after
// recordedAtIndexRaceDelay, doubling, at most recordedAtIndexRaceRepasses
// times; a replacement still unstamped after that waits for the next start.
const (
	recordedAtIndexRaceDelay    = 10 * time.Second
	recordedAtIndexRaceRepasses = 5
)

// startRecordedAtIndexBackfill runs the pass in the background at startup,
// retrying with backoff while it cannot complete, and again while a pass
// raced a concurrent write. Serving never waits on it: an entry without an
// instant reads as it always has, by its dateLabel.
func (rt *Runtime) startRecordedAtIndexBackfill(logger *log.Logger) {
	if rt == nil || rt.ctx == nil || rt.meetingMetadata == nil || rt.store == nil {
		return
	}
	wait := rt.recordedAtIndexWait
	if wait == nil {
		wait = sleepCtx
	}
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		delay := time.Minute
		raceDelay, repasses := recordedAtIndexRaceDelay, 0
		for {
			report, err := rt.backfillRecordedAtIndex(rt.ctx)
			if err == nil {
				rt.recordedAtIndexChecked.Store(true)
				if logger != nil {
					logger.Printf("recorded-at: meeting index pass done — %s", report)
				}
				rt.wakeRecordedAtFiles()
				if report.Raced == 0 {
					return
				}
				if repasses >= recordedAtIndexRaceRepasses {
					if logger != nil {
						logger.Printf("recorded-at: %d entries still raced after %d passes; the next start stamps them", report.Raced, repasses+1)
					}
					return
				}
				repasses++
				if logger != nil {
					logger.Printf("recorded-at: %d entries changed during the pass; running it again in %s", report.Raced, raceDelay)
				}
				if !wait(rt.ctx, raceDelay) {
					return
				}
				raceDelay *= 2
				continue
			}
			if rt.ctx.Err() != nil {
				return
			}
			if logger != nil {
				logger.Printf("recorded-at: meeting index pass failed (%v) after %s; retrying in %s", err, report, delay)
			}
			if !wait(rt.ctx, delay) {
				return
			}
			if delay < 10*time.Minute {
				delay *= 2
			}
		}
	}()
}

// recordedAtFileCounts is the file backfill's progress, by state.
func (s *meetingMetadataStore) recordedAtFileCounts(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	if s == nil {
		return counts, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT state, count(*) FROM recorded_at_file_backfill GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		counts[state] = n
	}
	return counts, rows.Err()
}

func formatStateCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}
