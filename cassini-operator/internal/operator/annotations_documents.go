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
	db, err := openSidecarAt(path, "annotations", true)
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
	if version > annotationsSchemaVersion || (version != 0 && version != 2 && version != 3 && version != 4) {
		return fail(fmt.Errorf("unsupported annotations schema %d; preserving database", version))
	}
	if version == annotationsSchemaVersion {
		return db, nil
	}
	err = db.inTx(context.Background(), func(tx *sql.Tx) error {
		if version < 3 {
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
		}
		if _, err := tx.Exec(`CREATE TABLE annotation_receipt (
   caller TEXT NOT NULL, request_id TEXT NOT NULL, opus_name TEXT NOT NULL,
   request_hash TEXT NOT NULL, response BLOB NOT NULL, snapshot INTEGER NOT NULL,
   created_at INTEGER NOT NULL DEFAULT (unixepoch()), PRIMARY KEY(caller,request_id)
  ); ALTER TABLE annotation_head ADD COLUMN rel_path TEXT NOT NULL DEFAULT '';
 CREATE TABLE annotation_tag_job(caller TEXT PRIMARY KEY,job_json BLOB NOT NULL,targets BLOB NOT NULL,op BLOB NOT NULL,titles BLOB NOT NULL);`); err != nil {
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
	var status annotationSyncStatus
	var generation string
	var blocked, attempts int
	err := s.db.QueryRowContext(ctx, `SELECT s.result_json,h.desired,h.confirmed,h.last_error,h.blocked,h.attempts,m.value
 FROM annotation_head h JOIN annotation_snapshot s ON s.id=h.desired JOIN annotations_meta m ON m.key='generation' WHERE h.opus_name=?`, name).Scan(&data, &status.Desired, &status.Confirmed, &status.Error, &blocked, &attempts, &generation)
	if err != nil {
		return annotateResult{}, err
	}
	var result annotateResult
	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	status.State = "saved"
	if status.Desired != status.Confirmed {
		status.State = "pending"
		if attempts > 0 {
			status.State = "delayed"
		}
		if blocked != 0 {
			status.State = "blocked"
		}
	}
	result.StateToken = fmt.Sprintf("%s:%d", generation, status.Desired)
	result.Sync = &status
	return result, nil
}

// Republish changes the audio metadata while pending annotation content stays
// owned by the DB. Create a new immutable head so clients see unresolved ranges.
func refreshAnnotationAudio(ctx context.Context, tx *sql.Tx, name string, delivered annotateResult) error {
	var data []byte
	if err := tx.QueryRowContext(ctx, `SELECT result_json FROM annotation_snapshot WHERE id=(SELECT desired FROM annotation_head WHERE opus_name=?)`, name).Scan(&data); err != nil {
		return err
	}
	var desired annotateResult
	if err := json.Unmarshal(data, &desired); err != nil {
		return err
	}
	if desired.AudioOpusSHA256 == delivered.AudioOpusSHA256 && desired.DurationMS == delivered.DurationMS {
		return nil
	}
	desired.AudioOpusSHA256 = delivered.AudioOpusSHA256
	desired.DurationMS = delivered.DurationMS
	desired.ContainerSHA256 = delivered.ContainerSHA256
	var doc projectedDocument
	if err := json.Unmarshal(desired.Annotations, &doc); err != nil {
		return err
	}
	resolved := doc.AudioOpusSHA256 == delivered.AudioOpusSHA256
	desired.Resolved = &resolved
	data, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	row, err := tx.ExecContext(ctx, `INSERT INTO annotation_snapshot(opus_name,result_json) VALUES(?,?)`, name, data)
	if err != nil {
		return err
	}
	id, err := row.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE annotation_head SET desired=? WHERE opus_name=?`, id, name); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE meeting_annotations SET resolved=?,container_sha256=? WHERE opus_name=?`, resolved, delivered.ContainerSHA256, name)
	return err
}

// Keep original responses for seven days, and longer while work is unresolved.
// Snapshot collection only removes states no durable pointer or receipt needs.
func (s *annotationStore) collectSnapshots(ctx context.Context) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM annotation_receipt WHERE created_at<unixepoch()-604800
 AND NOT EXISTS(SELECT 1 FROM annotation_head h WHERE h.opus_name=annotation_receipt.opus_name AND h.desired!=h.confirmed)
 AND NOT EXISTS(SELECT 1 FROM annotation_tag_job j WHERE j.caller=annotation_receipt.caller AND json_extract(j.job_json,'$.state')!='finished')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM annotation_snapshot WHERE id NOT IN (
 SELECT desired FROM annotation_head UNION SELECT confirmed FROM annotation_head UNION SELECT in_flight FROM annotation_head WHERE in_flight IS NOT NULL UNION SELECT snapshot FROM annotation_receipt)`)
		return err
	})
}
