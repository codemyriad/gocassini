package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// The search index (D-623): a SIDECAR SQLite database, beside jobs.sqlite3 and
// deliberately not inside it.
//
// Three reasons, in order of how much they cost to get wrong:
//
//  1. Contention. The job database opens with a bare path DSN, no pragmas and
//     SetMaxOpenConns(1) — so journal_mode is `delete`, busy_timeout is 0, and
//     every query serialises behind the publish writer. Putting search there
//     would queue each caller's query behind the pipeline, and would make this
//     ticket depend on fixing the job DB's pragmas first.
//  2. Lifecycle. This file is DISPOSABLE. Deleting it must cost nothing but a
//     rebuild, and it must never take job history with it.
//  3. Blast radius. A corrupt or oversized index is an outage for search only.
//
// THE INVARIANT, from which everything else follows:
//
//	The index is never the authority. Any state in it that cannot be rebuilt
//	from the published archive is a bug.
//
// That is why the schema carries no title, date, room or job id. Those live in
// the catalog and are joined in at request time from the CALLER'S OWN filtered
// catalog — which is also what makes this independent of where the catalog
// lives, so D-631 moving it later would change nothing here.
//
// It is also why meeting_index records ROW_SOURCE. A meeting indexed at publish
// time gets the producer's own segments, because the attempt bundle carries
// them; a meeting rebuilt from the published archive gets coarser word-derived
// rows, because the .opus carries words and nothing else. Both are legitimate;
// pretending they are the same would let an answer imply a precision it does
// not have. See search_rows.go for why re-deriving segments is not a fix.
//
// And it is why access control is absent from this file entirely. The index
// knows which meeting a row belongs to and nothing about who may read it; the
// visible set is an argument to the query, resolved per request from Nextcloud.
// A cache of that intersection here would be unsound in the permissive
// direction, because Nextcloud gives Cassini no permission-change signal.
const (
	// searchStoreFilename sits beside the job database in the same state dir.
	searchStoreFilename = "search.sqlite3"

	// searchSchemaVersion is stamped into PRAGMA user_version. Bump it for ANY
	// schema or windowing change — including a change to the window geometry,
	// which alters the row set without altering the DDL.
	searchSchemaVersion = 1

	// searchStateIndexed means the meeting's rows are present and current.
	searchStateIndexed = "indexed"
	// searchStateUnavailable means the meeting is known but NOT searchable.
	//
	// This state is the difference between a partial answer and a false one. If
	// ingest fails and the meeting simply keeps its previous row, coverage
	// reports full and a search says "no match in the 12 meetings you can read"
	// about a transcript that was never indexed. Recording the failure lets the
	// answer degrade to partial coverage instead, which a caller can act on.
	searchStateUnavailable = "unavailable"
)

// searchSchemaSQL is the whole schema. There are no migrations, by design: on a
// version mismatch the file is deleted and rebuilt (see openSearchStore).
//
// Real migrations would be machinery protecting data that is regenerable by
// definition, and they carry a worse failure mode — the operator's own
// migrations.go aborts startup on an unknown applied version, so an older
// binary meeting a newer index would refuse to start rather than rebuild.
const searchSchemaSQL = `
-- What the index knows about each meeting. References only: no title, date,
-- room or job id, so the catalog stays the sole owner of how a meeting is
-- described (see the invariant above).
CREATE TABLE IF NOT EXISTS meeting_index (
  -- path.Base of the delivered .opus. THE join key, and specifically not the
  -- catalog id or the job id: the per-caller visibility scan returns .opus
  -- basenames, and those three coincide only by convention.
  opus_name    TEXT PRIMARY KEY,
  state        TEXT NOT NULL,
  -- Why a meeting is unavailable, for the operator rather than the caller.
  reason       TEXT NOT NULL DEFAULT '',
  -- The digest of the artifact these rows were built from, so a rebuild can
  -- tell rows built from a DIFFERENT attempt from rows that are current.
  opus_sha256  TEXT NOT NULL DEFAULT '',
  -- Which payload the rows came from: the artifact's own readable segments, or
  -- a word-derived fallback for a meeting that carries none. Recorded so a
  -- result can say what granularity it is pointing at rather than implying they
  -- are the same.
  row_source   TEXT NOT NULL DEFAULT '',
  row_count    INTEGER NOT NULL DEFAULT 0,
  indexed_at   TEXT NOT NULL
);

-- One row per transcript segment. rowid is shared with segment_fts, which is
-- how a match resolves back to a reference.
CREATE TABLE IF NOT EXISTS segment_ref (
  rowid_     INTEGER PRIMARY KEY AUTOINCREMENT,
  opus_name  TEXT NOT NULL REFERENCES meeting_index(opus_name) ON DELETE CASCADE,
  -- The producer's own segment id, so a hit names the unit the viewer renders.
  -- Synthetic and deterministic for a meeting indexed from words instead.
  segment_id TEXT NOT NULL,
  start_ms   INTEGER NOT NULL,
  end_ms     INTEGER NOT NULL,
  speaker_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS segment_ref_by_meeting ON segment_ref(opus_name);

-- The inverted index. contentless (content='') so the store CANNOT emit the
-- text it indexed: SELECT text and snippet() both return NULL, which makes
-- references-only a property of the store rather than a rule in the handler.
--
-- Stated plainly because it is easy to over-claim: this is NOT a
-- confidentiality boundary. fts5vocab reconstructs the original wording, in
-- order, for anyone who can run SQL against this file. It earns its place on
-- size, on rebuildability, and on bounding what a HANDLER bug can disclose. The
-- text is already in the clear on the same volume regardless.
--
-- contentless_delete=1 so a meeting can be re-indexed or dropped; without it a
-- contentless table can only ever be appended to.
--
-- unicode61, not porter (unpredictable recall) and not trigram (much larger and
-- slower). This makes cross-meeting search token/prefix matching, while the
-- viewer's in-meeting search is substring — they will not return identical
-- results, and that is a deliberate choice rather than an oversight.
CREATE VIRTUAL TABLE IF NOT EXISTS segment_fts USING fts5(
  text,
  content='',
  contentless_delete=1,
  tokenize='unicode61'
);
`

// searchStore is the sidecar index.
type searchStore struct{ sidecarDB }

// searchStorePath is where the index lives for a given job-database path, or
// "" when that cannot be answered (sidecarPath).
func searchStorePath(dbPath string) string { return sidecarPath(dbPath, searchStoreFilename) }

func openSearchStore(path string, logger *log.Logger) (*searchStore, error) {
	db, err := openSidecarDB(path, "search index", searchSchemaSQL, searchSchemaVersion, logger)
	if err != nil {
		return nil, err
	}
	return &searchStore{db}, nil
}

// sidecarDB is a disposable SQLite file beside the job database: the search
// index and the tag index share it.
type sidecarDB struct {
	db   *sql.DB
	path string
}

// sidecarPath is filename beside the job database, or "" for a blank or
// relative job-database path. filepath.Dir("") is ".", which is how a test's
// zero Config once committed a 40 KB index into this package; "" makes the
// open refuse, which NewRuntime degrades gracefully.
func sidecarPath(dbPath, filename string) string {
	trimmed := strings.TrimSpace(dbPath)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return ""
	}
	return filepath.Join(filepath.Dir(trimmed), filename)
}

// openSidecarDB opens a sidecar, deleting and recreating it when its
// user_version is not version. Migrations would protect data that is
// regenerable by definition, and an older binary meeting a newer file must
// rebuild rather than refuse to start.
func openSidecarDB(path, what, schema string, version int, logger *log.Logger) (sidecarDB, error) {
	if strings.TrimSpace(path) == "" {
		return sidecarDB{}, fmt.Errorf("%s path must not be empty", what)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sidecarDB{}, fmt.Errorf("mkdir %s dir: %w", what, err)
	}
	s, err := openSidecarAt(path, what)
	if err != nil {
		return sidecarDB{}, err
	}
	current, err := s.userVersion()
	if err != nil {
		_ = s.Close()
		return sidecarDB{}, err
	}
	if current == version {
		return s, nil
	}
	// A brand-new file reads 0 and is simply stamped; a real mismatch is logged,
	// so an empty first answer after an upgrade is explicable.
	if current != 0 && logger != nil {
		logger.Printf("%s at %s is schema v%d, this build writes v%d — deleting and rebuilding", what, path, current, version)
	}
	if err := s.Close(); err != nil {
		return sidecarDB{}, fmt.Errorf("close stale %s: %w", what, err)
	}
	if err := removeSidecarFiles(path); err != nil {
		return sidecarDB{}, err
	}
	if s, err = openSidecarAt(path, what); err != nil {
		return sidecarDB{}, err
	}
	// The version goes LAST, so a crash midway leaves a file the next open
	// treats as fresh.
	if _, err := s.db.Exec(schema); err != nil {
		_ = s.Close()
		return sidecarDB{}, fmt.Errorf("create %s schema: %w", what, err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		_ = s.Close()
		return sidecarDB{}, fmt.Errorf("stamp %s schema version: %w", what, err)
	}
	return s, nil
}

// openSidecarAt opens the file with the pragmas the job database lacks: WAL so
// a reader never blocks the writer, a real busy_timeout, and foreign keys on.
// The pool stays at the driver default; WAL makes concurrent reads safe.
func openSidecarAt(path, what string) (sidecarDB, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return sidecarDB{}, fmt.Errorf("open %s: %w", what, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return sidecarDB{}, fmt.Errorf("ping %s: %w", what, err)
	}
	return sidecarDB{db: db, path: path}, nil
}

// removeSidecarFiles deletes the database and its WAL sidecars; a stale -wal
// left behind would be replayed into the new file.
func removeSidecarFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale index %s: %w", path+suffix, err)
		}
	}
	return nil
}

func (s sidecarDB) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s sidecarDB) userVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read %s version: %w", s.path, err)
	}
	return version, nil
}

// inTx runs fn in a transaction, rolling back on any error.
func (s sidecarDB) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s transaction: %w", s.path, err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s transaction: %w", s.path, err)
	}
	return nil
}

// ReplaceMeeting makes the index's rows for one meeting exactly `windows`.
//
// Replace, never append: re-indexing after a rerun must not leave the previous
// attempt's rows searchable alongside the current ones. The whole swap is one
// transaction, so a failure leaves the previous rows intact rather than a
// half-replaced meeting that would answer with a mixture of two attempts.
func (s *searchStore) ReplaceMeeting(ctx context.Context, opusName, opusSHA256, rowSource string, rows []searchRow) error {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return errors.New("opus name must not be empty")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := deleteMeetingRows(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO meeting_index (opus_name, state, reason, opus_sha256, row_source, row_count, indexed_at)
VALUES (?, ?, '', ?, ?, ?, ?)
ON CONFLICT(opus_name) DO UPDATE SET
  state = excluded.state, reason = '', opus_sha256 = excluded.opus_sha256,
  row_source = excluded.row_source, row_count = excluded.row_count,
  indexed_at = excluded.indexed_at`,
			name, searchStateIndexed, strings.TrimSpace(opusSHA256), rowSource, len(rows), nowUTCString()); err != nil {
			return fmt.Errorf("upsert meeting index row: %w", err)
		}

		refs, err := tx.PrepareContext(ctx,
			`INSERT INTO segment_ref (opus_name, segment_id, start_ms, end_ms, speaker_id) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare window insert: %w", err)
		}
		defer refs.Close()
		texts, err := tx.PrepareContext(ctx, `INSERT INTO segment_fts (rowid, text) VALUES (?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare posting insert: %w", err)
		}
		defer texts.Close()

		for _, row := range rows {
			result, err := refs.ExecContext(ctx, name, row.SegmentID, row.StartMS, row.EndMS, row.SpeakerID)
			if err != nil {
				return fmt.Errorf("insert segment: %w", err)
			}
			rowID, err := result.LastInsertId()
			if err != nil {
				return fmt.Errorf("read segment rowid: %w", err)
			}
			// The FTS row shares segment_ref's rowid; that shared key is the
			// only thing that turns a match back into a reference.
			if _, err := texts.ExecContext(ctx, rowID, row.Text); err != nil {
				return fmt.Errorf("insert posting: %w", err)
			}
		}
		return nil
	})
}

// MarkUnavailable records that a meeting is known but not searchable, dropping
// any rows it had. See searchStateUnavailable: this is what keeps a failed
// ingest reporting partial coverage instead of a confident false negative.
func (s *searchStore) MarkUnavailable(ctx context.Context, opusName, reason string) error {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return errors.New("opus name must not be empty")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := deleteMeetingRows(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO meeting_index (opus_name, state, reason, opus_sha256, row_source, row_count, indexed_at)
VALUES (?, ?, ?, '', '', 0, ?)
ON CONFLICT(opus_name) DO UPDATE SET
  state = excluded.state, reason = excluded.reason, row_source = '', row_count = 0,
  indexed_at = excluded.indexed_at`,
			name, searchStateUnavailable, strings.TrimSpace(reason), nowUTCString()); err != nil {
			return fmt.Errorf("mark meeting unavailable: %w", err)
		}
		return nil
	})
}

// ForgetMeeting removes a meeting from the index entirely.
//
// Housekeeping, never an access control. A recording the caller may not read
// already vanishes from their results because it vanishes from their visibility
// scan; removing its rows is about not indexing what no longer exists.
func (s *searchStore) ForgetMeeting(ctx context.Context, opusName string) error {
	name := strings.TrimSpace(opusName)
	if name == "" {
		return errors.New("opus name must not be empty")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := deleteMeetingRows(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM meeting_index WHERE opus_name = ?`, name); err != nil {
			return fmt.Errorf("delete meeting index row: %w", err)
		}
		return nil
	})
}

// deleteMeetingRows drops a meeting's segments and their postings.
//
// The postings go FIRST and by explicit rowid: segment_ref's ON DELETE CASCADE
// cannot reach segment_fts, which is a virtual table and has no foreign keys.
// Dropping the refs first would orphan every posting, and an orphaned posting
// in a contentless index is unreachable garbage that still matches.
func deleteMeetingRows(ctx context.Context, tx *sql.Tx, opusName string) error {
	rows, err := tx.QueryContext(ctx, `SELECT rowid_ FROM segment_ref WHERE opus_name = ?`, opusName)
	if err != nil {
		return fmt.Errorf("list existing segments: %w", err)
	}
	var rowIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan existing segment: %w", err)
		}
		rowIDs = append(rowIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("list existing segments: %w", err)
	}
	rows.Close()

	for _, id := range rowIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM segment_fts WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("delete posting: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM segment_ref WHERE opus_name = ?`, opusName); err != nil {
		return fmt.Errorf("delete segments: %w", err)
	}
	return nil
}

// searchCoverage is what the index can honestly claim to have searched.
//
// It exists so an answer can say what it covered rather than implying it
// covered everything. "No match in the 12 meetings you can read" is a different
// statement from "no match in the 9 of them that are indexed", and only the
// second is true when an ingest has failed.
type searchCoverage struct {
	Indexed     int
	Unavailable int
}

func (s *searchStore) Coverage(ctx context.Context) (searchCoverage, error) {
	var coverage searchCoverage
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM meeting_index GROUP BY state`)
	if err != nil {
		return searchCoverage{}, fmt.Errorf("read search coverage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return searchCoverage{}, fmt.Errorf("scan search coverage: %w", err)
		}
		switch state {
		case searchStateIndexed:
			coverage.Indexed = count
		case searchStateUnavailable:
			coverage.Unavailable = count
		}
	}
	if err := rows.Err(); err != nil {
		return searchCoverage{}, fmt.Errorf("read search coverage: %w", err)
	}
	return coverage, nil
}
