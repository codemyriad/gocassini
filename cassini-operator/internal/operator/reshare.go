package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
)

// Re-applying a published meeting's room audience (D-885).
//
// A rerun deliberately does not re-share an already published meeting, so
// that manual audience edits stick. That also leaves anyone Nextcloud refused
// at publication without access after the sharing policy is fixed. This is the
// explicit way back: an administrator asks, and the add-only reconciliation
// publication ran is applied once more. Nothing published changes — the shares
// live in Nextcloud, not in the meeting — but it can re-add a recipient an
// administrator removed by hand, which is why it is a button and never
// automatic.

type reshareResponse struct {
	Sharing *jobShareOutcome `json:"sharing"`
}

// errReshareNoCapturedAudience: the room's roster at the time of the meeting
// was never captured. Re-sharing with the room as it is now would reach people
// who were never in the meeting.
var errReshareNoCapturedAudience = errors.New("Cassini did not capture who was in this meeting's room, so it cannot tell who to share it with again. Share it from Files instead.")

func (rt *Runtime) handleReshareJob(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	job, err := rt.store.GetJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get job: %v", err))
		return
	}
	sink, ok := rt.sink().(*directSharesPublishSink)
	if !ok {
		writeJSONError(w, http.StatusConflict, "Only meetings published to Nextcloud are shared with their room.")
		return
	}
	// Whether the meeting was ever published, not how the latest attempt
	// went: a rerun or a summary that failed afterwards leaves it published.
	published, err := rt.store.HasSuccessfulPublish(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !published {
		writeJSONError(w, http.StatusConflict, "Only a published meeting can be shared again.")
		return
	}
	// Not while an attempt is under way: its publication may be replacing the
	// file, and it reconciles the shares itself where that is due.
	if !reshareJobAtRest(job) {
		writeJSONError(w, http.StatusConflict, "This meeting is being processed again. Share it again once that finishes.")
		return
	}
	outcome, err := rt.store.JobShareOutcome(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if outcome != nil && outcome.Status == shareOutcomeStarterRefused {
		writeJSONError(w, http.StatusConflict, "The last publication stopped because Nextcloud refused the person who started the recording. Change the sharing setting, then rerun it.")
		return
	}
	fresh, err := sink.reshare(ctx, id)
	var retired *meetingRetiredError
	switch {
	case errors.As(err, &retired):
		writeJSONError(w, http.StatusConflict, retired.message())
		return
	case errors.Is(err, errReshareNoCapturedAudience):
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		rt.logger.Printf("reshare job=%s: %v", id, err)
		writeJSONError(w, http.StatusBadGateway, "Cassini could not share the meeting again. Check Doctor's Recording storage and Recording sharing, then try again.")
		return
	}
	if fresh != nil {
		fresh.AudienceCount = capturedAudienceCount(job)
	}
	// Other open panels learn of it as of any change to the run.
	rt.store.emitStateChange(ctx, "job.updated", id, job.CurrentAttemptNumber)
	writeJSON(w, http.StatusOK, reshareResponse{Sharing: fresh})
}

// reshareJobAtRest mirrors the panel's notion of a run that is not active: a
// finished one, one waiting on a blocked build, or one a restart interrupted.
func reshareJobAtRest(job Job) bool {
	return job.Stage == "done" || job.State == "blocked" || job.State == "interrupted"
}

type meetingRetiredError struct{ reason string }

func (e *meetingRetiredError) Error() string { return "meeting retired: " + e.reason }

func (e *meetingRetiredError) message() string {
	if e.reason == meetingRetireReasonDeleted {
		return "This meeting was deleted."
	}
	return "This meeting has expired."
}

// reshare reconciles the delivered meeting's shares with the room audience
// captured for it, and records what Nextcloud refused — the starter included:
// the meeting is published already, so nobody's refusal can unpublish it.
// It is also how recipients Nextcloud's limit on new shares held back are
// reached: it walks everyone again, so a re-share that gets through clears
// them, and one that meets the limit again leaves the rest deferred.
func (s *directSharesPublishSink) reshare(ctx context.Context, jobID string) (*jobShareOutcome, error) {
	for _, name := range []string{jobID + ".opus", jobID + ".json"} {
		if reason := s.cfg.retiredMeetingReason(ctx, name); reason != "" {
			return nil, &meetingRetiredError{reason: reason}
		}
	}
	if _, captured, err := s.rt.store.JobRoomAudience(ctx, jobID); err != nil {
		return nil, err
	} else if !captured {
		return nil, errReshareNoCapturedAudience
	}
	if !ncAccessSubstrate.usable() {
		s.cfg.preflightDirectShares(ctx, s.rt.logger)
	}
	if !ncAccessSubstrate.usable() {
		snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
		return nil, fmt.Errorf("Nextcloud recording shares are not ready (%s: %s)", snap.Step, snap.Detail)
	}
	remote, err := s.deliveredMeetingPath(ctx, jobID)
	if err != nil {
		return nil, err
	}
	// The same lock publication and retirement take, so a retirement cannot
	// start between the check below and the shares being made.
	release, err := annotationWriteLocks.acquire(ctx, path.Base(remote))
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.cfg.meetingNotRetired(ctx, path.Base(remote)); err != nil {
		if errors.Is(err, errMeetingRetired) {
			return nil, &meetingRetiredError{reason: s.cfg.retiredMeetingReason(ctx, path.Base(remote))}
		}
		return nil, err
	}
	audience, public, err := s.audienceForJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	note := s.rt.meetingFactsFor(ctx, jobID).shareNote(false)
	skipped, err := s.cfg.reconcileRecordingShares(ctx, s.client, remote, audience, public, shareReapply, note)
	if err != nil {
		return nil, err
	}
	before, _ := s.rt.store.JobShareOutcome(ctx, jobID)
	if err := s.rt.store.RecordJobShareOutcome(ctx, jobID, shareOutcomeStatus(skipped), skipped); err != nil {
		return nil, err
	}
	outcome, err := s.rt.store.JobShareOutcome(ctx, jobID)
	if err != nil {
		return nil, err
	}
	// The shares.completed sent at publication may have said deferred or
	// partial. A re-share that changes who has the meeting is news to a
	// receiver; one that changes nothing is not. The payload is the one
	// publication sends, audience count included.
	if !sameShareOutcome(before, outcome) {
		if job, jobErr := s.rt.store.GetJob(ctx, jobID); jobErr == nil {
			if sharing := s.rt.jobSharing(ctx, &job); sharing != nil {
				s.rt.emitEvent(eventSharesCompleted, jobID, job.CurrentAttemptNumber, map[string]any{
					"sharing":    sharing,
					"reconciled": true,
				})
			}
		}
	}
	return outcome, nil
}

// deliveredMeetingPath is where publication put the job's meeting: an audio
// recording or a transcript-only one.
func (s *directSharesPublishSink) deliveredMeetingPath(ctx context.Context, jobID string) (string, error) {
	for _, ext := range []string{".opus", ".json"} {
		remote := ncRecordingsRoot + "/meetings/" + jobID + ext
		state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, remote)
		if err != nil {
			return "", fmt.Errorf("inspect delivered recording: %w", err)
		}
		if state.Exists {
			return remote, nil
		}
	}
	return "", fmt.Errorf("the published meeting %s is not in Cassini's recordings folder", jobID)
}

// sameShareOutcome reports whether two share outcomes say the same thing to a
// reader: the same status and the same people left out. When each was
// recorded is not part of it.
func sameShareOutcome(a, b *jobShareOutcome) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, _ := json.Marshal(jobShareOutcome{Status: a.Status, Skipped: a.Skipped, Deferred: a.Deferred})
	right, _ := json.Marshal(jobShareOutcome{Status: b.Status, Skipped: b.Skipped, Deferred: b.Deferred})
	return bytes.Equal(left, right)
}
