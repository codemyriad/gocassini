package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// meetingMetadataStore describes files already stored in Nextcloud. It never
// records recipients or decides which caller can see a row. Meeting rows are
// derived; room_names in the SAME database are durable user data.
const (
	meetingMetadataFilename = "meetings.sqlite3"
	meetingMetadataVersion  = 2
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

type meetingMetadataStore struct {
	sidecarDB
	lifecycle *Store
}

func meetingMetadataPath(dbPath string) string { return sidecarPath(dbPath, meetingMetadataFilename) }

func openMeetingMetadataStore(file string, logger *log.Logger) (*meetingMetadataStore, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	db, err := openSidecarAt(file, "meeting metadata and room names", true)
	if err != nil {
		return nil, err
	}
	var tx *sql.Tx
	fail := func(err error) (*meetingMetadataStore, error) {
		if tx != nil {
			_ = tx.Rollback()
		}
		_ = db.Close()
		return nil, err
	}
	tx, err = db.db.Begin()
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version < 0 || version > meetingMetadataVersion {
		return fail(fmt.Errorf("unsupported meetings database version %d; preserving database", version))
	}
	if version < meetingMetadataVersion {
		if _, err := tx.Exec(meetingMetadataSchema + roomNamesSchema); err != nil {
			return fail(err)
		}
		if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	store := &meetingMetadataStore{sidecarDB: db}
	if err := store.registerMetadataRooms(context.Background()); err != nil {
		return fail(err)
	}
	return store, nil
}

func (s *meetingMetadataStore) Put(ctx context.Context, fileID int64, opusName string, entry json.RawMessage) error {
	if s != nil && s.lifecycle != nil {
		release, err := meetingProjectionLocks.acquire(ctx, opusName)
		if err != nil {
			return err
		}
		defer release()
		if err := (ExAppConfig{lifecycle: s.lifecycle}).meetingNotRetired(ctx, opusName); err != nil {
			return err
		}
	}

	if s == nil || fileID <= 0 || !isMeetingFile(opusName) || path.Base(opusName) != opusName {
		return fmt.Errorf("invalid meeting metadata key")
	}
	var probe struct {
		DateLabel   string `json:"dateLabel"`
		AudioPath   string `json:"audioPath"`
		MeetingPath string `json:"meetingPath"`
	}
	if err := json.Unmarshal(entry, &probe); err != nil {
		return fmt.Errorf("decode meeting metadata: %w", err)
	}
	if catalogEntryOpusName(meetingPath(probe.AudioPath, probe.MeetingPath), "") != opusName {
		return fmt.Errorf("meeting metadata does not describe %s", opusName)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_metadata
  (file_id, opus_name, date_label, entry_json, indexed_at)
  VALUES (?, ?, ?, ?, ?)
  ON CONFLICT(file_id) DO UPDATE SET
    opus_name=excluded.opus_name, date_label=excluded.date_label,
    entry_json=excluded.entry_json, indexed_at=excluded.indexed_at`,
		fileID, opusName, probe.DateLabel, string(entry), nowUTCString())
	if err != nil {
		return err
	}
	var room roomEntry
	if json.Unmarshal(entry, &room) == nil {
		return s.registerRoom(ctx, room.RoomID, room.RoomName)
	}
	return nil
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

// FileIDFor is the Nextcloud file id of the delivered meeting named opusName,
// or 0 when the index has none. A meeting re-delivered after its file was
// removed by hand gets a new id while the old row may linger, so the most
// recently indexed row wins.
func (s *meetingMetadataStore) FileIDFor(ctx context.Context, opusName string) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var fileID int64
	err := s.db.QueryRowContext(ctx, `SELECT file_id FROM meeting_metadata WHERE opus_name = ? ORDER BY indexed_at DESC, file_id DESC LIMIT 1`, opusName).Scan(&fileID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return fileID, err
}

func (s *meetingMetadataStore) Forget(ctx context.Context, fileID int64) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM meeting_metadata WHERE file_id = ?`, fileID)
	return err
}
