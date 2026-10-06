package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *annotationService) resumeRemoteRetirement(ctx context.Context, op remoteRetentionOperation) error {
	store := s.rt.annotationReads()
	if store == nil {
		return fmt.Errorf("annotation store unavailable")
	}
	release, err := annotationMutationLocks.acquire(ctx, store.path)
	if err != nil {
		return err
	}
	_, err = s.rt.store.db.ExecContext(ctx, `UPDATE meeting_lifecycle SET state='retiring' WHERE name=?`, op.Name)
	release()
	if err != nil {
		return err
	}
	var status string
	if err = s.rt.store.db.QueryRowContext(ctx, `SELECT status FROM remote_retention_operation WHERE name=?`, op.Name).Scan(&status); err != nil {
		return err
	}
	state, err := s.exapp.davRetentionLeaf(ctx, s.client, op.Source)
	if err != nil {
		return err
	}
	if state.Exists {
		if state.FileID != op.FileID || state.ETag != op.InputETag {
			return fmt.Errorf("retirement source changed")
		}
		if err = s.rt.store.saveRemoteOperation(ctx, op, "delete-intent"); err != nil {
			return err
		}
		if err = s.exapp.davRetentionDelete(ctx, s.client, op.Source, state.ETag); err != nil {
			return err
		}
		after, err := s.exapp.davRetentionLeaf(ctx, s.client, op.Source)
		if err != nil {
			return err
		}
		if after.Exists {
			return fmt.Errorf("retired file is still present")
		}
	} else if status != "delete-intent" && status != "deleted" && status != "cleanup" {
		return fmt.Errorf("missing file without recorded deletion intent")
	}
	if err = s.rt.store.saveRemoteOperation(ctx, op, "deleted"); err != nil {
		return err
	}
	// Tombstone denies every serving path while independent stores are scrubbed.
	if _, err = s.rt.store.db.ExecContext(ctx, `UPDATE meeting_lifecycle SET state='retired' WHERE name=?`, op.Name); err != nil {
		return err
	}
	projectionRelease, err := meetingProjectionLocks.acquire(ctx, op.Name)
	if err != nil {
		return err
	}
	if s.rt.meetingMetadata != nil {
		if err = s.rt.meetingMetadata.Forget(ctx, op.FileID); err != nil {
			projectionRelease()
			return err
		}
	}
	if s.rt.searchStore != nil {
		if err = s.rt.searchStore.ForgetMeeting(ctx, op.Name); err != nil {
			projectionRelease()
			return err
		}
	}
	projectionRelease()
	release, err = annotationMutationLocks.acquire(ctx, store.path)
	if err != nil {
		return err
	}
	defer release()
	err = store.inTx(ctx, func(tx *sql.Tx) error {
		if err := deleteAnnotationRows(ctx, tx, op.Name); err != nil {
			return err
		}
		// Keep single-request hashes and explicit terminal outcomes for idempotency.
		if _, err := tx.ExecContext(ctx, `UPDATE annotation_receipt SET response=?,snapshot=0 WHERE opus_name=?`, []byte(`{"expired":true}`), op.Name); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT caller,request_id,response FROM annotation_batch_receipt WHERE (caller,request_id) IN (SELECT caller,request_id FROM annotation_batch_target WHERE opus_name=?)`, op.Name)
		if err != nil {
			return err
		}
		type receipt struct {
			caller, id string
			raw        []byte
		}
		receipts := []receipt{}
		for rows.Next() {
			var r receipt
			if err := rows.Scan(&r.caller, &r.id, &r.raw); err != nil {
				rows.Close()
				return err
			}
			receipts = append(receipts, r)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, r := range receipts {
			raw, err := scrubRetiredResult(r.raw, op.Name)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE annotation_batch_receipt SET response=? WHERE caller=? AND request_id=?`, raw, r.caller, r.id); err != nil {
				return err
			}
		}
		tagRows, err := tx.QueryContext(ctx, `SELECT caller,titles,job_json FROM annotation_tag_job`)
		if err != nil {
			return err
		}
		type tagRecord struct {
			caller      string
			titles, job []byte
		}
		tagRecords := []tagRecord{}
		for tagRows.Next() {
			var r tagRecord
			if err := tagRows.Scan(&r.caller, &r.titles, &r.job); err != nil {
				tagRows.Close()
				return err
			}
			tagRecords = append(tagRecords, r)
		}
		if err := tagRows.Close(); err != nil {
			return err
		}
		for _, r := range tagRecords {
			var titles map[string]string
			if err := json.Unmarshal(r.titles, &titles); err != nil {
				return err
			}
			title, exists := titles[op.Name]
			if !exists {
				continue
			}
			delete(titles, op.Name)
			var job tagJob
			if err := json.Unmarshal(r.job, &job); err != nil {
				return err
			}
			for i := range job.Failed {
				if job.Failed[i].Meeting == title || job.Failed[i].Meeting == op.Name {
					job.Failed[i] = tagJobFailure{Meeting: op.Name, Error: "expired"}
				}
			}
			{
				s.jobs.mu.Lock()
				if live := s.jobs.last[r.caller]; live != nil && live.ID == job.ID {
					for i := range live.Failed {
						if live.Failed[i].Meeting == title || live.Failed[i].Meeting == op.Name {
							live.Failed[i] = tagJobFailure{Meeting: op.Name, Error: "expired"}
						}
					}
				}
				s.jobs.mu.Unlock()
			}
			labels, _ := json.Marshal(titles)
			data, _ := json.Marshal(job)
			if _, err := tx.ExecContext(ctx, `UPDATE annotation_tag_job SET titles=?,job_json=?,op=CASE WHEN ? THEN ? ELSE op END WHERE caller=?`, labels, data, len(titles) == 0, []byte(`{"ops":[]}`), r.caller); err != nil {
				return err
			}
		}
		for _, query := range []string{`DELETE FROM annotation_batch_target WHERE opus_name=?`, `DELETE FROM annotation_head WHERE opus_name=?`, `DELETE FROM annotation_snapshot WHERE opus_name=?`} {
			if _, err := tx.ExecContext(ctx, query, op.Name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.finishRemoteRetention(ctx, op)
}

// Remove this meeting's response content without touching other batch targets.
func scrubRetiredResult(raw []byte, name string) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	id := meetingStem(name)
	var scrub func(any) any
	scrub = func(v any) any {
		switch item := v.(type) {
		case map[string]any:
			if target, ok := item["meetingId"].(string); ok && (target == id || target == name) {
				return map[string]any{"meetingId": target, "expired": true}
			}
			for k, child := range item {
				item[k] = scrub(child)
			}
		case []any:
			for i, child := range item {
				item[i] = scrub(child)
			}
		}
		return v
	}
	return json.Marshal(scrub(value))
}
