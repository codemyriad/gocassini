package operator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Unlike search projections, this database will contain acknowledged edits.
// Unknown versions fail closed; migrations never unlink the database or WAL.
func openDurableAnnotationDB(path string) (sidecarDB, error) {
	if strings.TrimSpace(path) == "" {
		return sidecarDB{}, fmt.Errorf("annotations path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return sidecarDB{}, err
	}
	db, err := openSidecarAt(path, "annotations")
	if err != nil {
		return db, err
	}
	fail := func(err error) (sidecarDB, error) { _ = db.Close(); return sidecarDB{}, err }
	db.db.SetMaxOpenConns(1)
	if _, err = db.db.Exec(`PRAGMA synchronous=FULL`); err != nil {
		return fail(err)
	}
	version, err := db.userVersion()
	if err != nil {
		return fail(err)
	}
	if version > annotationsSchemaVersion || (version != 0 && version != 2 && version != 3) {
		return fail(fmt.Errorf("unsupported annotations schema %d; preserving database", version))
	}
	if version == annotationsSchemaVersion {
		return db, nil
	}
	err = db.inTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(annotationsSchemaSQL); err != nil {
			return err
		}
		if _, err := tx.Exec(`
CREATE TABLE annotation_snapshot (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 opus_name TEXT NOT NULL,
 result_json BLOB NOT NULL,
 created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE annotation_head (
 opus_name TEXT PRIMARY KEY,
 desired INTEGER NOT NULL REFERENCES annotation_snapshot(id),
 confirmed INTEGER NOT NULL REFERENCES annotation_snapshot(id),
 in_flight INTEGER REFERENCES annotation_snapshot(id),
 input_etag TEXT NOT NULL DEFAULT '',
 output_sha256 TEXT NOT NULL DEFAULT '',
 output_size INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '',
 blocked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX annotation_snapshot_meeting ON annotation_snapshot(opus_name);
DELETE FROM annotations_meta WHERE key='built';
`); err != nil {
			return err
		}
		var generation [16]byte
		if _, err := rand.Read(generation[:]); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO annotations_meta(key,value) VALUES('generation',?)`, hex.EncodeToString(generation[:])); err != nil {
			return err
		}
		_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", annotationsSchemaVersion))
		return err
	})
	if err != nil {
		return fail(err)
	}
	return db, nil
}

func importAnnotationSnapshot(ctx context.Context, tx *sql.Tx, name string, result annotateResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	// Imports are snapshots of delivered files, not a reconstructed edit history.
	row, err := tx.ExecContext(ctx, `INSERT INTO annotation_snapshot(opus_name,result_json) VALUES(?,?)`, name, data)
	if err != nil {
		return err
	}
	id, err := row.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO annotation_head(opus_name,desired,confirmed) VALUES(?,?,?)
 ON CONFLICT(opus_name) DO UPDATE SET desired=excluded.desired,confirmed=excluded.confirmed`, name, id, id)
	return err
}

func (s *annotationStore) document(ctx context.Context, name string) (annotateResult, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT s.result_json FROM annotation_head h JOIN annotation_snapshot s ON s.id=h.desired WHERE h.opus_name=?`, name).Scan(&data)
	if err != nil {
		return annotateResult{}, err
	}
	var result annotateResult
	err = json.Unmarshal(data, &result)
	return result, err
}
