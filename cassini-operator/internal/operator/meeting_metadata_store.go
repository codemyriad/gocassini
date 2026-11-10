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
	// The meeting's instant (recordedAtUtc, normalised to
	// recordedAtUTCLayout; '' when the entry has none) beside its label, and
	// the progress of writing it back into delivered files (which an
	// administrator starts and pauses: recorded_at_migration.go). Purely additive,
	// so it is applied on every open and user_version stays 2: a released
	// operator refuses a newer version, and this schema is one it can run on
	// unchanged, so a rollback keeps the room names and the index. The
	// backfill fills the column (recorded_at_index.go).
	meetingMetadataRecordedAtSchema = `
CREATE INDEX IF NOT EXISTS meeting_metadata_by_recorded_at ON meeting_metadata(recorded_at_utc);
CREATE TABLE IF NOT EXISTS recorded_at_file_backfill (
  opus_name       TEXT PRIMARY KEY,
  recorded_at_utc TEXT NOT NULL,
  state           TEXT NOT NULL DEFAULT 'pending',
  attempts        INTEGER NOT NULL DEFAULT 0,
  retry_at        INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT NOT NULL DEFAULT '',
  updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS recorded_at_file_backfill_due ON recorded_at_file_backfill(state, retry_at);
CREATE TABLE IF NOT EXISTS recorded_at_file_backfill_control (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  paused     INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT ''
);
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
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", meetingMetadataVersion)); err != nil {
			return fail(err)
		}
	}
	var hasColumn int
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('meeting_metadata') WHERE name='recorded_at_utc'`).Scan(&hasColumn); err != nil {
		return fail(err)
	}
	if hasColumn == 0 {
		if _, err := tx.Exec(`ALTER TABLE meeting_metadata ADD COLUMN recorded_at_utc TEXT NOT NULL DEFAULT ''`); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.Exec(meetingMetadataRecordedAtSchema); err != nil {
		return fail(err)
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
	// The instant is stored as the entry states it, nothing derived here: an
	// entry without one is stamped by the backfill or the publish sink.
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_metadata
  (file_id, opus_name, date_label, entry_json, indexed_at, recorded_at_utc)
  VALUES (?, ?, ?, ?, ?, ?)
  ON CONFLICT(file_id) DO UPDATE SET
    opus_name=excluded.opus_name, date_label=excluded.date_label,
    entry_json=excluded.entry_json, indexed_at=excluded.indexed_at,
    recorded_at_utc=excluded.recorded_at_utc`,
		fileID, opusName, probe.DateLabel, string(entry), nowUTCString(), normalizedRecordedAtUTC(entryRecordedAtUTC(entry)))
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

// entriesByName is the indexed entry of each delivered meeting named, by its
// file name — for many meetings at once, where FileIDFor and EntriesFor would
// be two queries each. As in FileIDFor, the most recently indexed row for a
// name wins. Names the index does not know are absent.
func (s *meetingMetadataStore) entriesByName(ctx context.Context, names []string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(names))
	if s == nil || len(names) == 0 {
		return out, nil
	}
	err := forEachSQLBatch(names, func(marks string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT opus_name, entry_json FROM meeting_metadata WHERE opus_name IN (`+marks+`) ORDER BY indexed_at DESC, file_id DESC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, raw string
			if err := rows.Scan(&name, &raw); err != nil {
				return err
			}
			if _, have := out[name]; !have {
				out[name] = json.RawMessage(raw)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Forget drops a meeting's entry and any recordedAtUtc file work queued for
// it: a retired or deleted file is never re-tagged, and a missing one must not
// sit in the backfill as blocked.
func (s *meetingMetadataStore) Forget(ctx context.Context, fileID int64) error {
	if s == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM recorded_at_file_backfill WHERE opus_name IN (SELECT opus_name FROM meeting_metadata WHERE file_id = ?)`, fileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meeting_metadata WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	return tx.Commit()
}
