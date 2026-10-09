package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GET and POST annotations/meetings/<id>/speakers — who spoke, as people
// corrected it (docs/speaker-separation.md).
//
// Same caller resolution, visibility and status discipline as the marks next
// to it: anyone who can read a meeting can say a device was shared and name
// the voices, and a meeting outside the caller's readable set is a 404
// identical to one that does not exist. The edits document lives in the
// operator's job store, keyed by the job the meeting was published from; a
// POST stores the next revision and queues the refine attempt that applies it,
// in one transaction, and answers before any of it runs.

const (
	maxSpeakerEditsBodyBytes = 64 << 10
	speakerEditsReadTimeout  = 30 * time.Second
)

const (
	speakerReasonNoJob           = "no-job"
	speakerReasonNoSourceAudio   = "no-source-audio"
	speakerReasonNoTranscript    = "no-transcript"
	speakerReasonDiarizationUnav = "diarization-unavailable"
	// speakerReasonUnpublishedRebuild: the job's last rebuild (a rerun) was
	// never published, so the meeting a refine would copy is not the one
	// readers have. An administrator's rerun clears it.
	speakerReasonUnpublishedRebuild = "unpublished-rebuild"
	speakerStateIdle                = "idle"
	speakerStateApplying            = "applying"
	speakerStateFailed              = "failed"
	speakerStateUnavailable         = "unavailable"
	speakerTranscriptRawASR         = "transcript.raw-asr.words.v1.json"
	speakerTranscriptPrimary        = "transcript.words.v1.json"
)

// speakerEditsResponse is the GET shape, and what a successful POST answers.
type speakerEditsResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	// ReasonDetail says, for diarization-unavailable, why and how an
	// administrator installs the model. Empty otherwise.
	ReasonDetail    string               `json:"reasonDetail,omitempty"`
	Revision        int                  `json:"revision"`
	AppliedRevision int                  `json:"appliedRevision"`
	State           string               `json:"state"`
	LastError       string               `json:"lastError"`
	Doc             speakerEditsDoc      `json:"doc"`
	Participants    []speakerParticipant `json:"participants"`
	// Report is the CLI's report of the last apply that was published, or null.
	Report json.RawMessage `json:"report"`
	// Progress is where the pending refine is and how long it should take
	// in all; null unless State is applying.
	Progress *speakerEditsProgress `json:"progress"`
}

// speakerEditsProgress tells the page what an applying edit is waiting for.
// EstimatedMs is the expected time from the attempt's start to republished:
// the work only, never the wait behind other builds or a recording, and it
// stays the same for the whole attempt. ElapsedMs is measured from when the
// attempt was queued while Phase is queued (how long it has waited), and from
// when it started once it has (build_started_at), so the page counts down
// EstimatedMs-ElapsedMs on its own clock between polls only once the work
// runs, and a long queue never eats the countdown.
type speakerEditsProgress struct {
	Phase       string `json:"phase"`
	ElapsedMs   int64  `json:"elapsedMs"`
	EstimatedMs int64  `json:"estimatedMs"`
}

const (
	// speakerPhaseQueued: the refine attempt has not started; it waits behind
	// another build or a recording.
	speakerPhaseQueued = "queued"
	// speakerPhaseSeparating: the attempt runs and a split it applies has no
	// stored turns yet, so the diarizer is working.
	speakerPhaseSeparating = "separating"
	// speakerPhaseUpdating: apply, summary, seal and publish.
	speakerPhaseUpdating = "updating"

	// Everything but diarization — apply with its summary rewrite, seal and
	// publish — is speakerRefineBaseMs plus speakerRefineFixedRate per audio
	// millisecond: measured about 3.5 s on a 3-minute meeting and 14 s on a
	// 64-minute one. The base is also the floor: an edit of a meeting whose
	// length is unknown is estimated at 3 s, no more, so a short meeting's
	// countdown does not promise twice its real time.
	speakerRefineBaseMs    = 3000
	speakerRefineFixedRate = 0.003
	// Diarization time per audio millisecond, decoding included. The default
	// is what a CPU operator measured before it has run any of its own.
	speakerDiarizeRateDefault = 0.016
	speakerDiarizeRateMin     = 0.005
	speakerDiarizeRateMax     = 0.1
	// speakerDiarizeDecodeFactor turns a stored set's elapsedMs, the model's
	// own time, into the whole step's: decoding the track adds about 15%.
	speakerDiarizeDecodeFactor = 1.15
)

// speakerParticipant is one device of the original transcript: what can be
// split. Never a voice.
type speakerParticipant struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// speakerEditsRequest is the POST body. The doc's own format and revision are
// the server's to set, so they are accepted and ignored.
type speakerEditsRequest struct {
	ExpectRevision *int `json:"expectRevision"`
	Doc            *struct {
		Format   string              `json:"format"`
		Revision int                 `json:"revision"`
		Splits   []speakerEditsSplit `json:"splits"`
		Merges   []speakerEditsMerge `json:"merges"`
		Labels   []speakerEditsLabel `json:"labels"`
	} `json:"doc"`
}

func (s *annotationService) routeSpeakers(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	if s.rt == nil || s.rt.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "speaker edits unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.readSpeakers(w, r, caller, meetingID)
	case http.MethodPost:
		s.writeSpeakers(w, r, caller, meetingID)
	default:
		writeMethodNotAllowed(w, "GET, POST")
	}
}

func (s *annotationService) readSpeakers(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	ctx, cancel := context.WithTimeout(r.Context(), speakerEditsReadTimeout)
	defer cancel()
	opusName, ok := s.authorizedSpeakersRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	state, err := s.rt.speakerEditsState(ctx, speakerJobID(opusName))
	if err != nil {
		s.logf("annotations: speakers meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "speaker edits unavailable")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *annotationService) writeSpeakers(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	expectRevision, doc, refusal := readSpeakerEditsRequest(w, r)
	if refusal != "" {
		writeSpeakerEditsInvalid(w, refusal)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), speakerEditsReadTimeout)
	defer cancel()
	opusName, ok := s.authorizedSpeakersRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	jobID := speakerJobID(opusName)
	state, err := s.rt.speakerEditsState(ctx, jobID)
	if err != nil {
		s.logf("annotations: speakers meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "speaker edits unavailable")
		return
	}
	switch state.Reason {
	case speakerReasonNoJob:
		// A recording with no job cannot be split, and saying so here would
		// be a statement about the archive's internals; it is a meeting this
		// surface does not know.
		s.answerFailure(w, r, "speakers meeting="+meetingID, annotateNotFound(
			fmt.Errorf("meeting=%s has no operator job (served as 404)", meetingID)))
		return
	case speakerReasonNoSourceAudio, speakerReasonNoTranscript, speakerReasonUnpublishedRebuild:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "unavailable", "reason": state.Reason})
		return
	}
	participants := map[string]bool{}
	for _, p := range state.Participants {
		participants[p.ID] = true
	}
	if err := doc.validate(participants); err != nil {
		writeSpeakerEditsInvalid(w, err.Error())
		return
	}
	// The edits the recording already carries, sent again: nothing to apply,
	// so nothing is queued — no republish, no summary model call. A failed
	// revision is not applied, so sending it again is still a retry.
	if expectRevision == state.Revision && state.Revision == state.AppliedRevision &&
		state.State != speakerStateApplying && state.State != speakerStateFailed && sameSpeakerEdits(doc, state.Doc) {
		writeJSON(w, http.StatusOK, state)
		return
	}
	missing, err := s.rt.store.MissingSpeakerSplitTurns(ctx, jobID, doc)
	if err != nil {
		s.logf("annotations: speakers meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "speaker edits unavailable")
		return
	}
	if len(missing) > 0 {
		if ok, detail := s.rt.diarizationAvailability(ctx); !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": speakerReasonDiarizationUnav, "detail": detail})
			return
		}
	}

	revision, err := s.rt.store.QueueSpeakerEdits(ctx, jobID, expectRevision, doc, caller, formatUTCString(s.rt.speakerNow()))
	switch {
	case errors.Is(err, errSpeakerEditsRevisionConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "revision-conflict", "revision": revision})
		return
	case errors.Is(err, errSpeakerEditsBusy):
		writeJSONError(w, http.StatusConflict, "busy")
		return
	case errors.Is(err, errSpeakerEditsSourceExpired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "unavailable", "reason": speakerReasonNoSourceAudio})
		return
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
		return
	case err != nil:
		s.logf("annotations: speakers meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "speaker edits unavailable")
		return
	}
	s.rt.dispatchQueuedRefine(jobID)
	s.logf("annotations: speakers meeting=%s revision=%d queued by %s", meetingID, revision, caller)

	state, err = s.rt.speakerEditsState(ctx, jobID)
	if err != nil {
		s.logf("annotations: speakers meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "speaker edits unavailable")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// authorizedSpeakersRecording resolves meetingID to the caller's recording
// the way marks do: in their readable set, and readable by them in Nextcloud
// right now. A POST republishes the recording for every reader with the
// caller's names in it, so a share Nextcloud stopped honouring must not do
// here what it cannot do to a mark.
func (s *annotationService) authorizedSpeakersRecording(ctx context.Context, w http.ResponseWriter, r *http.Request, caller, meetingID string) (string, bool) {
	_, opusName, _, ok := s.visibleRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return "", false
	}
	if _, err := s.authorizeRecording(ctx, caller, opusName); err != nil {
		s.answerFailure(w, r, "speakers meeting="+meetingID, err)
		return "", false
	}
	return opusName, true
}

// dispatchQueuedRefine hands the job's freshly queued attempt to a build
// worker without blocking the request; the requeue dispatcher delivers it
// otherwise, as it does every rerun (D-367).
func (rt *Runtime) dispatchQueuedRefine(jobID string) {
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil || job.ArtifactRunPath == nil {
		rt.kickRequeueScan()
		return
	}
	task := buildTask{JobID: job.ID, AttemptNumber: job.CurrentAttemptNumber, ArtifactRunPath: *job.ArtifactRunPath}
	select {
	case rt.buildQueue <- task:
	default:
		rt.kickRequeueScan()
	}
}

func writeSpeakerEditsInvalid(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "message": message})
}

// readSpeakerEditsRequest parses the body strictly. The returned message is
// about the caller's own body, so it is safe to return.
func readSpeakerEditsRequest(w http.ResponseWriter, r *http.Request) (int, speakerEditsDoc, string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSpeakerEditsBodyBytes))
	if err != nil {
		return 0, speakerEditsDoc{}, "the request body could not be read"
	}
	var request speakerEditsRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		return 0, speakerEditsDoc{}, `the request body must be {"expectRevision":n,"doc":{"splits":[…],"merges":[…],"labels":[…]}}`
	}
	if request.ExpectRevision == nil || *request.ExpectRevision < 0 {
		return 0, speakerEditsDoc{}, "expectRevision is required"
	}
	if request.Doc == nil {
		return 0, speakerEditsDoc{}, "doc is required"
	}
	doc := speakerEditsDoc{
		Format: speakerEditsFormat,
		Splits: request.Doc.Splits,
		Merges: request.Doc.Merges,
		Labels: request.Doc.Labels,
	}.normalized()
	return *request.ExpectRevision, doc, ""
}

// speakerJobID is the job a visible recording was published from: the direct
// shares sink delivers exactly meetings/<jobID>.opus.
func speakerJobID(opusName string) string {
	jobID := strings.TrimSuffix(opusName, ".opus")
	if !isPlainMeetingID(jobID) {
		return ""
	}
	return jobID
}

// speakerEditsState is what the page is told about one job's speakers.
func (rt *Runtime) speakerEditsState(ctx context.Context, jobID string) (speakerEditsResponse, error) {
	resp := speakerEditsResponse{Doc: emptySpeakerEditsDoc(), Participants: []speakerParticipant{}}
	if jobID == "" {
		return resp.unavailable(speakerReasonNoJob), nil
	}
	job, err := rt.store.GetJob(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return resp.unavailable(speakerReasonNoJob), nil
	}
	if err != nil {
		return resp, err
	}
	rec, err := rt.store.GetSpeakerEdits(ctx, jobID)
	if err != nil {
		return resp, err
	}
	resp.Revision, resp.AppliedRevision, resp.Doc = rec.Revision, rec.AppliedRevision, rec.Doc
	resp.Report = rec.LastReport

	meetingPath := canonicalMeetingPath(rt.cfg.WorkRoot, jobID)
	if participants, err := readSpeakerParticipants(meetingPath); err == nil {
		resp.Participants = participants
	}
	unpublished, err := rt.speakerMeetingUnpublished(ctx, jobID)
	if err != nil {
		return resp, err
	}
	switch {
	case !speakerSourceAudioReady(job) || rt.store.artifactSourceExpired(ctx, jobID):
		// Retention records an expired capture as such: the same reason,
		// whatever the job row still names.
		resp.Reason = speakerReasonNoSourceAudio
	case len(resp.Participants) == 0 || speakerMeetingUntranscribed(meetingPath):
		// A build that kept only the audio still lists every participant,
		// with no words to separate or name.
		resp.Reason = speakerReasonNoTranscript
	case unpublished:
		resp.Reason = speakerReasonUnpublishedRebuild
	default:
		// Only a new split needs the model; with turns already stored, naming,
		// merging and undoing still work, so they are not reported unavailable.
		hasTurns, err := rt.store.HasSpeakerSplitTurns(ctx, jobID)
		if err != nil {
			return resp, err
		}
		if !hasTurns {
			if ok, detail := rt.diarizationAvailability(ctx); !ok {
				resp.Reason, resp.ReasonDetail = speakerReasonDiarizationUnav, detail
			}
		}
	}
	resp.Available = resp.Reason == ""

	resp.State = speakerStateIdle
	if !resp.Available {
		resp.State = speakerStateUnavailable
	}
	attempt, ok, err := rt.store.LatestSpeakerEditsAttempt(ctx, jobID)
	if err != nil {
		return resp, err
	}
	switch {
	case ok && attempt.Revision > rec.AppliedRevision && (attempt.State == "queued" || attempt.State == "running"):
		resp.State = speakerStateApplying
		if resp.Progress, err = rt.speakerRefineProgress(ctx, jobID, attempt); err != nil {
			return resp, err
		}
	case rec.Revision > rec.AppliedRevision:
		// Saved and not being applied, so its attempt ended without
		// publishing it: failed at any stage, interrupted, blocked, or
		// followed by a rerun that replayed the revision the recording
		// carries. The failure stands until a new revision applies.
		resp.State = speakerStateFailed
		cause := rec.LastError
		if cause == "" && ok && attempt.Revision > rec.AppliedRevision {
			cause = attempt.Error
		}
		resp.LastError = publicSpeakerEditsError(cause)
	}
	return resp, nil
}

// publicSpeakerEditsError is what a reader is told about an apply that
// failed. The cause — CLI output naming the operator's paths, a Nextcloud
// answer — is for the operator's log and the attempt row, never for every
// reader of the meeting.
func publicSpeakerEditsError(cause string) string {
	switch {
	case strings.Contains(cause, "diarization-unavailable"):
		return "Voice separation is not available on this server."
	case strings.Contains(cause, "speaker-not-found"):
		return "This participant's own audio is not in the recording."
	case strings.Contains(cause, "turns-source-mismatch"):
		return "The voices found earlier were found in a different recording."
	case strings.Contains(cause, "audio identity changed under a speaker edit"):
		return "Updating the recording would have lost its marks, so it was left as it was."
	case strings.Contains(cause, speakerReasonUnpublishedRebuild):
		return "The recording was processed again and not published; an administrator can rerun it."
	}
	return "The recording could not be updated."
}

// sameSpeakerEdits reports whether two documents ask for the same thing,
// whatever their order and stamps.
func sameSpeakerEdits(a, b speakerEditsDoc) bool {
	key := func(d speakerEditsDoc) string {
		var parts []string
		for _, s := range d.Splits {
			parts = append(parts, "s\x00"+s.SpeakerID)
		}
		for _, m := range d.Merges {
			parts = append(parts, "m\x00"+m.From+"\x00"+m.Into)
		}
		for _, l := range d.Labels {
			parts = append(parts, "l\x00"+l.SpeakerID+"\x00"+l.Label)
		}
		sort.Strings(parts)
		return strings.Join(parts, "\x01")
	}
	return key(a) == key(b)
}

// speakerMeetingUntranscribed reports whether the meeting bundle was built
// without a transcript (transcription skipped or failed), as its manifest
// says. A manifest that says nothing is from a build that always transcribed.
func speakerMeetingUntranscribed(meetingPath string) bool {
	var manifest struct {
		Processing *struct {
			Transcription struct {
				Status string `json:"status"`
			} `json:"transcription"`
		} `json:"processing"`
	}
	raw, err := os.ReadFile(filepath.Join(meetingPath, "manifest.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Processing == nil {
		return false
	}
	status := manifest.Processing.Transcription.Status
	return status != "" && status != "completed"
}

// speakerMeetingUnpublished reports whether current/<job>.meeting, the bundle
// a refine copies, is a rebuild that was never published: current/ follows
// the last attempt that BUILT, and a rerun whose seal or publish failed left
// its new transcript and re-encoded audio there while readers still have the
// recording before it. A refine copied from it would publish that rebuild
// under the name of a speaker edit. A refine's own bundle is a copy of one
// that passed this check, with the same audio, so it never counts. A bundle
// with no attempt stamp, or an attempt still on its way, says nothing.
func (rt *Runtime) speakerMeetingUnpublished(ctx context.Context, jobID string) (bool, error) {
	var stamp struct {
		AttemptNumber int `json:"attempt_number"`
	}
	raw, err := os.ReadFile(filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, jobID), "cassini.json"))
	if err != nil || json.Unmarshal(raw, &stamp) != nil || stamp.AttemptNumber <= 0 {
		return false, nil
	}
	var kind, state string
	err = rt.store.db.QueryRowContext(ctx, `
SELECT trigger_kind, state FROM job_attempts WHERE job_id = ? AND attempt_number = ?`, jobID, stamp.AttemptNumber).Scan(&kind, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load the attempt that built %s: %w", jobID, err)
	}
	return kind != triggerKindRefine && (state == "failed" || state == "interrupted"), nil
}

// speakerNow is the speaker edits surface's clock.
func (rt *Runtime) speakerNow() time.Time {
	if rt.speakerClock != nil {
		return rt.speakerClock()
	}
	return time.Now()
}

// speakerRefineProgress reports the phase of the pending refine attempt and
// its estimate. The estimate depends only on what was known when the attempt
// was queued — which splits had no turns then, the meeting's length, and the
// turn sets stored before it — so it does not move while the attempt runs,
// not even when its own diarization stores a new turn set. Elapsed time
// counts from the queue while the attempt waits and from its start after.
func (rt *Runtime) speakerRefineProgress(ctx context.Context, jobID string, attempt speakerEditsAttempt) (*speakerEditsProgress, error) {
	queuedAt, queuedKnown := time.Time{}, false
	if at, err := parseInsightTime(attempt.QueuedAt); err == nil {
		queuedAt, queuedKnown = at, true
	}
	stored, err := rt.store.SpeakerSplitTurnsStoredAt(ctx, jobID)
	if err != nil {
		return nil, err
	}
	// Every split without turns at queue time is diarized, one after another,
	// each over its own full-length track.
	missingNow, diarizes := false, 0
	counted := map[string]bool{}
	for _, split := range attempt.Doc.Splits {
		if counted[split.SpeakerID] {
			continue
		}
		counted[split.SpeakerID] = true
		at, ok := stored[split.SpeakerID]
		if !ok {
			missingNow = true
			diarizes++
			continue
		}
		// Stored since the attempt was queued: by this attempt, which had to
		// diarize when it was queued.
		if t, err := parseInsightTime(at); err == nil && queuedKnown && !t.Before(queuedAt) {
			diarizes++
		}
	}

	progress := &speakerEditsProgress{Phase: speakerPhaseUpdating}
	switch {
	case attempt.Stage == "build" && attempt.State == "queued":
		progress.Phase = speakerPhaseQueued
	case missingNow:
		progress.Phase = speakerPhaseSeparating
	}
	since, sinceKnown := queuedAt, queuedKnown
	if progress.Phase != speakerPhaseQueued {
		if at, err := parseInsightTime(attempt.StartedAt); err == nil {
			since, sinceKnown = at, true
		}
	}
	if sinceKnown {
		progress.ElapsedMs = max(rt.speakerNow().Sub(since).Milliseconds(), 0)
	}

	audioMs := readSpeakerMeetingAudioMs(canonicalMeetingPath(rt.cfg.WorkRoot, jobID))
	rate := 0.0
	if diarizes > 0 {
		runs, err := rt.store.SpeakerDiarizationRuns(ctx)
		if err != nil {
			return nil, err
		}
		rate = learnedSpeakerDiarizeRate(runs, queuedAt, queuedKnown)
	}
	progress.EstimatedMs = speakerRefineEstimateMs(audioMs, rate, diarizes)
	return progress, nil
}

// learnedSpeakerDiarizeRate is this operator's diarization time per audio
// millisecond: the median of its stored turn sets' elapsedMs/durationMs, plus
// decoding, within bounds that keep one odd run from promising seconds or
// hours. Only sets stored before the attempt was queued count.
func learnedSpeakerDiarizeRate(runs []speakerDiarizationRun, before time.Time, beforeKnown bool) float64 {
	ratios := make([]float64, 0, len(runs))
	for _, run := range runs {
		if run.ElapsedMs <= 0 || run.DurationMs <= 0 {
			continue
		}
		if at, err := parseInsightTime(run.StoredAt); err == nil && beforeKnown && !at.Before(before) {
			continue
		}
		ratios = append(ratios, float64(run.ElapsedMs)/float64(run.DurationMs))
	}
	if len(ratios) == 0 {
		return speakerDiarizeRateDefault
	}
	sort.Float64s(ratios)
	median := ratios[len(ratios)/2]
	if len(ratios)%2 == 0 {
		median = (ratios[len(ratios)/2-1] + ratios[len(ratios)/2]) / 2
	}
	return min(max(median*speakerDiarizeDecodeFactor, speakerDiarizeRateMin), speakerDiarizeRateMax)
}

// speakerRefineFixedMs is the part of a refine that is not diarization:
// apply with its summary rewrite, seal and publish, for a meeting of audioMs.
func speakerRefineFixedMs(audioMs int64) int64 {
	return speakerRefineBaseMs + int64(float64(max(audioMs, 0))*speakerRefineFixedRate+0.5)
}

// speakerRefineEstimateMs is the expected time from the attempt's start to
// republished: one diarization of the meeting's length for each of the
// diarizes splits it has to separate, at rate, plus the fixed part.
func speakerRefineEstimateMs(audioMs int64, rate float64, diarizes int) int64 {
	estimate := speakerRefineFixedMs(audioMs)
	if diarizes > 0 && audioMs > 0 {
		estimate += int64(float64(diarizes)*float64(audioMs)*rate + 0.5)
	}
	return estimate
}

// readSpeakerMeetingAudioMs is the meeting's length: the bundle manifest's
// source durationMs, or else its transcript's media.durationMs. Zero when
// neither says.
func readSpeakerMeetingAudioMs(meetingPath string) int64 {
	var manifest struct {
		Source struct {
			DurationMs int64 `json:"durationMs"`
		} `json:"source"`
	}
	if raw, err := os.ReadFile(filepath.Join(meetingPath, "manifest.json")); err == nil && json.Unmarshal(raw, &manifest) == nil && manifest.Source.DurationMs > 0 {
		return manifest.Source.DurationMs
	}
	for _, name := range []string{speakerTranscriptRawASR, speakerTranscriptPrimary} {
		var transcript struct {
			Media struct {
				DurationMs int64 `json:"durationMs"`
			} `json:"media"`
		}
		if raw, err := os.ReadFile(filepath.Join(meetingPath, name)); err == nil && json.Unmarshal(raw, &transcript) == nil && transcript.Media.DurationMs > 0 {
			return transcript.Media.DurationMs
		}
	}
	return 0
}

func (r speakerEditsResponse) unavailable(reason string) speakerEditsResponse {
	r.Available, r.Reason, r.State = false, reason, speakerStateUnavailable
	return r
}

// speakerSourceAudioReady reports whether the job still has the capture a
// split diarizes: its ready current/<job>.run.
func speakerSourceAudioReady(job Job) bool {
	if job.ArtifactRunPath == nil || strings.TrimSpace(*job.ArtifactRunPath) == "" {
		return false
	}
	manifest, err := readRunManifest(filepath.Join(*job.ArtifactRunPath, "cassini.json"))
	return err == nil && manifest.State == bundleStateReady && manifest.Stage == "ready"
}

// readSpeakerParticipants reads the original roster of a meeting bundle: the
// raw-ASR transcript once a split exists, the primary one before.
func readSpeakerParticipants(meetingPath string) ([]speakerParticipant, error) {
	raw, err := os.ReadFile(filepath.Join(meetingPath, speakerTranscriptRawASR))
	if errors.Is(err, os.ErrNotExist) {
		raw, err = os.ReadFile(filepath.Join(meetingPath, speakerTranscriptPrimary))
	}
	if err != nil {
		return nil, err
	}
	var transcript struct {
		Speakers []speakerParticipant `json:"speakers"`
	}
	if err := json.Unmarshal(raw, &transcript); err != nil {
		return nil, err
	}
	participants := make([]speakerParticipant, 0, len(transcript.Speakers))
	for _, p := range transcript.Speakers {
		// "merged" is the mixed track a thin per-participant pass falls back
		// to: no participant's own audio, so nothing a split can diarize.
		if p.ID == "" || isSpeakerVoiceID(p.ID) || strings.EqualFold(p.ID, "merged") {
			continue
		}
		participants = append(participants, p)
	}
	return participants, nil
}
