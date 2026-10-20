package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// remoteRetentionActionDelete is the journaled action for a user-initiated
// delete. It runs the same cascade as a retention "retire" — conditional owner
// DELETE into Nextcloud's trashbin, tombstone, projection scrub — and differs
// only in its trigger, its wording and its lifecycle reason.
const remoteRetentionActionDelete = "delete"

// anchorSourceDeleteRequest marks a lifecycle row adopted only to carry a
// delete. Its anchor is the request time, never an original age: retention
// math trusts only createdAtUtc/recordedAtLocal sources.
const anchorSourceDeleteRequest = "deleteRequest"

// errMeetingDeleteBusy: a stage worker, cleanup or lifecycle operation holds
// the job right now. The delete was not started; the caller may retry.
var errMeetingDeleteBusy = errors.New("meeting is busy")

// deleteMeeting answers POST annotations/meetings/<id>/delete.
//
// Status discipline is the write path's: denial is a 404 identical to a
// meeting that does not exist — a replayed delete lands here too, because a
// deleted meeting is no longer in anyone's visible set. 409 means the job is
// busy right now and the delete was not started; the caller may retry.
func (s *annotationService) deleteMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()
	_, opusName, _, ok := s.visibleRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	// Re-check as the caller, right now, exactly as a mark write would: the
	// share list a request starts from can be stale.
	if _, err := s.authorizeRecording(ctx, caller, opusName); err != nil {
		s.answerFailure(w, r, "delete meeting="+meetingID, err)
		return
	}
	s.logf("annotations: delete meeting=%s requested by caller=%s", meetingID, caller)
	if err := s.prepareMeetingDeletion(ctx, opusName); err != nil {
		if errors.Is(err, errMeetingDeleteBusy) {
			writeJSONError(w, http.StatusConflict, "The meeting is busy; try again in a moment.")
			return
		}
		s.answerFailure(w, r, "delete meeting="+meetingID, annotateUnavailable(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meetingId": meetingID, "deleted": true})
}

// prepareMeetingDeletion deletes one published meeting on a user's behalf.
//
// Unlike prepareRemoteRetention it has no retention-settings revision to pin,
// no capability certification gate (the conditional fileId+ETag checks carry
// the safety, exactly as the annotation write-back PUTs do on every install),
// and no requirement that a job row exists — an imported meeting has none.
// The caller has already been authorized against the meeting.
func (s *annotationService) prepareMeetingDeletion(ctx context.Context, name string) error {
	if logicalMeetingName(name) != name || name == "" {
		return fmt.Errorf("invalid meeting name")
	}
	unlock, ok := s.rt.store.tryLockArtifacts(meetingStem(name))
	if !ok {
		return errMeetingDeleteBusy
	}
	defer unlock()
	release, err := annotationWriteLocks.acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	// A pending operation — an expiry or an earlier delete — already owns the
	// outcome; finishing it IS the deletion.
	var raw []byte
	err = s.rt.store.db.QueryRowContext(ctx, `SELECT operation_json FROM remote_retention_operation WHERE name=? AND status!='completed'`, name).Scan(&raw)
	switch {
	case err == nil:
		var op remoteRetentionOperation
		if err := json.Unmarshal(raw, &op); err != nil {
			return err
		}
		return s.resumeRemoteRetention(ctx, op)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	current, ok, err := s.rt.store.meetingLifecycle(ctx, name)
	if err != nil {
		return err
	}
	if ok && (current.State == "retired" || current.State == "retiring") {
		// Already tombstoned with nothing pending: the meeting is gone.
		return nil
	}
	if !ok {
		// Most installs never ran a retention sweep, so the meeting has no
		// lifecycle row yet. Adopt one now, anchored at the request.
		leaf := ncRecordingsRoot + "/meetings/" + name
		state, err := s.exapp.davRetentionLeaf(ctx, s.client, leaf)
		if err != nil {
			return err
		}
		if !state.Exists {
			return fmt.Errorf("managed file identity changed or is missing")
		}
		current = meetingLifecycle{Name: name, FileID: state.FileID, Path: leaf, State: "active", Anchor: nowUTCString(), AnchorSource: anchorSourceDeleteRequest}
		if err := s.rt.store.adoptMeetingLifecycle(ctx, current); err != nil {
			return err
		}
	}
	state, err := s.exapp.davRetentionLeaf(ctx, s.client, current.Path)
	if err != nil {
		return err
	}
	if !state.Exists || state.FileID != current.FileID {
		return fmt.Errorf("managed file identity changed or is missing")
	}
	op := remoteRetentionOperation{Name: name, Action: remoteRetentionActionDelete, Source: current.Path, FileID: current.FileID, InputETag: state.ETag}
	// Serialize tombstone installation with annotation acceptance — the same
	// discipline as prepareRemoteRetention — before recording irreversible work.
	store := s.rt.annotationReads()
	if store == nil {
		return fmt.Errorf("annotation store unavailable")
	}
	mutation, err := annotationMutationLocks.acquire(ctx, store.path)
	if err != nil {
		return err
	}
	err = func() error {
		tx, err := s.rt.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		raw, err := json.Marshal(op)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO remote_retention_operation(name,operation_json,status,updated_at) VALUES(?,?,'prepared',?) ON CONFLICT(name) DO UPDATE SET operation_json=excluded.operation_json,status='prepared',last_error='',updated_at=excluded.updated_at`, op.Name, raw, nowUTCString()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE meeting_lifecycle SET state='retiring',reason=? WHERE name=?`, meetingRetireReasonDeleted, op.Name); err != nil {
			return err
		}
		return tx.Commit()
	}()
	mutation()
	if err != nil {
		return remoteRetentionJournalError{err}
	}
	return s.resumeRemoteRetention(ctx, op)
}
