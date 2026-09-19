package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// prepareAnnotationRepublish makes a replacement discoverable even when the old
// head was clean. A failed/lost PUT must be reconciled before calling it saved.
// The caller holds the recording lock through upload and acknowledgement.
func (s *annotationStore) prepareAnnotationRepublish(ctx context.Context, name string, result annotateResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE annotation_head SET republish_json=?,blocked=0,retry_at=0,attempts=0,last_error='' WHERE opus_name=?`, data, name)
	return err
}

// recordAnnotationDelivery is for a verified operator file replacement, not an
// import observation. Its baseline supersedes the old confirmed snapshot even
// if the replacement has no annotations and a lower revision.
func (s *annotationStore) recordAnnotationDelivery(ctx context.Context, name string, delivered annotateResult) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var data []byte
		err := tx.QueryRowContext(ctx, `SELECT s.result_json FROM annotation_head h JOIN annotation_snapshot s ON s.id=h.desired WHERE h.opus_name=?`, name).Scan(&data)
		var desired annotateResult
		if err == nil {
			err = json.Unmarshal(data, &desired)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// A new recording identity explicitly discards all old annotations,
		// including pending edits. Same-audio repairs preserve the latest head.
		if errors.Is(err, sql.ErrNoRows) || desired.AudioOpusSHA256 != delivered.AudioOpusSHA256 || sameAnnotationDocument(desired.Annotations, delivered.Annotations) {
			if err := importAnnotationSnapshot(ctx, tx, name, delivered); err != nil {
				return err
			}
			if err := replaceAnnotationProjection(ctx, tx, name, projectAnnotations(delivered.Annotations, delivered.Resolved, delivered.AudioOpusSHA256), delivered.ContainerSHA256, annotationsStateIndexed); err != nil {
				return err
			}
		} else {
			encoded, err := json.Marshal(delivered)
			if err != nil {
				return err
			}
			row, err := tx.ExecContext(ctx, `INSERT INTO annotation_snapshot(opus_name,result_json) VALUES(?,?)`, name, encoded)
			if err != nil {
				return err
			}
			id, err := row.LastInsertId()
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE annotation_head SET confirmed=? WHERE opus_name=?`, id, name); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE annotation_head SET republish_json=NULL,in_flight=NULL,input_etag='',output_sha256='',output_size=0,blocked=0,retry_at=0,attempts=0,last_error='' WHERE opus_name=?`, name)
		return err
	})
}
