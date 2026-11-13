package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
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
	case errors.Is(err, errReshareModeratorsUnreadable):
		writeJSONError(w, http.StatusBadGateway, err.Error())
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
	_, captured, err := s.rt.store.JobRoomAudience(ctx, jobID)
	if err != nil {
		return nil, err
	}
	var moderatorsOf talkRecordingBinding
	if !captured {
		// Refused here for every mode but moderators; the room is read further
		// down, once nothing else can stop the re-share.
		if moderatorsOf, err = s.moderatorRoomForReshare(ctx, jobID); err != nil {
			return nil, err
		}
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
	if !captured {
		if err := s.captureModeratorsForReshare(ctx, jobID, moderatorsOf); err != nil {
			return nil, err
		}
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
	if err := s.rt.store.RecordJobShareOutcome(ctx, jobID, shareOutcomeStatus(skipped), skipped); err != nil {
		return nil, err
	}
	return s.rt.store.JobShareOutcome(ctx, jobID)
}

// A moderator-mode recording with no saved roster was shared with the starter
// only, because the room's moderators were never read during the call. An
// administrator asking for a re-share is the one place they are read
// afterwards: a role, unlike who was in a call, is still a fair answer later
// (#480). Every other mode keeps refusing, as before.
//
// moderatorRoomForReshare says whether that applies and which room to ask.
func (s *directSharesPublishSink) moderatorRoomForReshare(ctx context.Context, jobID string) (talkRecordingBinding, error) {
	job, err := s.rt.store.GetJob(ctx, jobID)
	if err != nil {
		return talkRecordingBinding{}, err
	}
	if strings.TrimSpace(job.AudienceMode) != shareAudienceModerators || s.rt.fetchTalkModerators == nil {
		return talkRecordingBinding{}, errReshareNoCapturedAudience
	}
	binding, ok := s.rt.talkBindingForJob(jobID)
	if !ok {
		return talkRecordingBinding{}, errReshareNoCapturedAudience
	}
	return binding, nil
}

// errReshareModeratorsUnreadable: Talk would not say who moderates the room,
// so the re-share shared with nobody.
var errReshareModeratorsUnreadable = errors.New("Cassini could not read this room's moderators from Talk, so nothing was shared. Check that the conversation still exists, then try again.")

// captureModeratorsForReshare reads the room's moderators and saves them as
// the recording's roster. From then on room_audience_at is when an
// administrator asked, not a moment during the meeting, and later re-shares
// use this saved list.
func (s *directSharesPublishSink) captureModeratorsForReshare(ctx context.Context, jobID string, binding talkRecordingBinding) error {
	ctx, cancel := context.WithTimeout(ctx, roomAudienceTimeout)
	defer cancel()
	moderators, _, err := s.rt.resolveRecordingAudienceWith(ctx, jobID, binding.Owner, binding.RoomToken, s.rt.fetchTalkModerators)
	if err != nil {
		s.rt.logger.Printf("reshare job=%s: read moderators: %v", jobID, err)
		return errReshareModeratorsUnreadable
	}
	return s.rt.store.MergeJobRoomAudience(ctx, jobID, moderators, nowUTCString())
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
