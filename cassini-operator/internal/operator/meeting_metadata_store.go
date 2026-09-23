package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"
)

// meetingMetadataStore describes files already stored in Nextcloud. It never
// records recipients or decides which caller can see a row. The complete index
// can be discarded and rebuilt from owner inventory and portable recordings.
const (
	meetingMetadataFilename = "meetings.sqlite3"
	meetingMetadataVersion  = 1
	meetingMetadataSchema   = `
CREATE TABLE IF NOT EXISTS meeting_metadata (
  file_id     INTEGER PRIMARY KEY,
  opus_name   TEXT NOT NULL,
  date_label  TEXT NOT NULL DEFAULT '',
  entry_json  TEXT NOT NULL,
  indexed_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS meeting_metadata_by_name ON meeting_metadata(opus_name);
CREATE INDEX IF NOT EXISTS meeting_metadata_by_date ON meeting_metadata(date_label);
`
)

type meetingMetadataStore struct{ sidecarDB }

func meetingMetadataPath(dbPath string) string { return sidecarPath(dbPath, meetingMetadataFilename) }

func openMeetingMetadataStore(file string, logger *log.Logger) (*meetingMetadataStore, error) {
	db, err := openSidecarDB(file, "meeting metadata", meetingMetadataSchema, meetingMetadataVersion, logger)
	if err != nil {
		return nil, err
	}
	return &meetingMetadataStore{db}, nil
}

func (s *meetingMetadataStore) Put(ctx context.Context, fileID int64, opusName string, entry json.RawMessage) error {
	if s == nil || fileID <= 0 || !strings.HasSuffix(opusName, ".opus") || path.Base(opusName) != opusName {
		return fmt.Errorf("invalid meeting metadata key")
	}
	var probe struct {
		DateLabel string `json:"dateLabel"`
		AudioPath string `json:"audioPath"`
	}
	if err := json.Unmarshal(entry, &probe); err != nil {
		return fmt.Errorf("decode meeting metadata: %w", err)
	}
	if catalogEntryOpusName(probe.AudioPath, "") != opusName {
		return fmt.Errorf("meeting metadata does not describe %s", opusName)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_metadata
  (file_id, opus_name, date_label, entry_json, indexed_at)
  VALUES (?, ?, ?, ?, ?)
  ON CONFLICT(file_id) DO UPDATE SET
    opus_name=excluded.opus_name, date_label=excluded.date_label,
    entry_json=excluded.entry_json, indexed_at=excluded.indexed_at`,
		fileID, opusName, probe.DateLabel, string(entry), nowUTCString())
	return err
}

// EntriesFor returns only locally described file IDs supplied by a fresh OCS
// share snapshot. A missing row cannot grant access, and a row for an unshared
// file is never selected. Batches stay below older SQLite variable limits.
func (s *meetingMetadataStore) EntriesFor(ctx context.Context, fileIDs []int64) (map[int64]json.RawMessage, error) {
	out := make(map[int64]json.RawMessage, len(fileIDs))
	if s == nil || len(fileIDs) == 0 {
		return out, nil
	}
	for start := 0; start < len(fileIDs); start += 500 {
		end := min(start+500, len(fileIDs))
		args := make([]any, 0, end-start)
		marks := make([]string, 0, end-start)
		for _, id := range fileIDs[start:end] {
			if id <= 0 {
				continue
			}
			args = append(args, id)
			marks = append(marks, "?")
		}
		if len(args) == 0 {
			continue
		}
		rows, err := s.db.QueryContext(ctx, `SELECT file_id, entry_json FROM meeting_metadata WHERE file_id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fileID int64
			var raw string
			if err := rows.Scan(&fileID, &raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[fileID] = json.RawMessage(raw)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *meetingMetadataStore) Forget(ctx context.Context, fileID int64) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM meeting_metadata WHERE file_id = ?`, fileID)
	return err
}

// AllEntries supports administrative index rebuilds. Visibility endpoints must
// use EntriesFor with a fresh share snapshot instead.
func (s *meetingMetadataStore) AllEntries(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT entry_json FROM meeting_metadata ORDER BY date_label DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []json.RawMessage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		entries = append(entries, json.RawMessage(raw))
	}
	return entries, rows.Err()
}
