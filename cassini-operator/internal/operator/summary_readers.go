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
	"net/url"
	"strings"
	"sync"
	"time"
)

// A meeting's summary, as its readers see and ask for it (D-814).
//
// Who may do what:
//   - writing a missing summary: anyone who can read the meeting. It fills a
//     gap, at most once, and the cost is shown before it is sent.
//   - replacing an existing one (Regenerate, or rewriting it with speaker
//     edits): an administrator, or a moderator of the meeting's Talk room,
//     the people Talk lets start and stop its recordings
//     (RecordingController's RequireLoggedInModeratorParticipant). It changes
//     what every reader sees.

// summaryRewriteCheck says whether caller may replace jobID's summary.
type summaryRewriteCheck func(ctx context.Context, caller, jobID string) (bool, error)

// summaryRewritePermissions caches the check for a minute per caller and
// meeting: a page asks on every load, and the answer comes from two
// Nextcloud requests.
type summaryRewritePermissions struct {
	mu     sync.Mutex
	check  summaryRewriteCheck
	ttl    time.Duration
	now    func() time.Time
	cached map[string]summaryRewriteVerdict
}

type summaryRewriteVerdict struct {
	allowed bool
	until   time.Time
}

func newSummaryRewritePermissions(check summaryRewriteCheck) *summaryRewritePermissions {
	return &summaryRewritePermissions{check: check, ttl: time.Minute, now: time.Now, cached: map[string]summaryRewriteVerdict{}}
}

// allowed answers false when the check fails: a refusal the person can ask an
// administrator about beats a rewrite nobody permitted. The failure is logged.
func (p *summaryRewritePermissions) allowed(ctx context.Context, caller, jobID string, logf func(string, ...any)) bool {
	if p == nil || p.check == nil || caller == "" || jobID == "" {
		return false
	}
	key := caller + "\x00" + jobID
	now := p.now()
	p.mu.Lock()
	if v, ok := p.cached[key]; ok && now.Before(v.until) {
		p.mu.Unlock()
		return v.allowed
	}
	p.mu.Unlock()
	ok, err := p.check(ctx, caller, jobID)
	if err != nil {
		logf("summary: rewrite permission for %s on %s: %v (refused)", caller, jobID, err)
		return false
	}
	p.mu.Lock()
	p.cached[key] = summaryRewriteVerdict{allowed: ok, until: now.Add(p.ttl)}
	p.mu.Unlock()
	return ok
}

// Talk's participant types that moderate a room. A guest moderator (6) is not
// a Nextcloud user and cannot reach Cassini, and Talk lets only logged-in
// moderators record.
const (
	talkParticipantOwner     = 1
	talkParticipantModerator = 2
)

// nextcloudSummaryRewriteCheck asks Nextcloud, as the caller: an administrator
// is answered by the admin group listing (2xx only for an administrator, as
// resolveAdminIdentity relies on); a moderator by the meeting's Talk room.
func (s *annotationService) nextcloudSummaryRewriteCheck(ctx context.Context, caller, jobID string) (bool, error) {
	status, _, err := s.exapp.apiGetAs(ctx, s.client, caller, s.exapp.ocsURL("/cloud/groups/admin"))
	if err != nil {
		return false, fmt.Errorf("admin check: %w", err)
	}
	if status/100 == 2 {
		return true, nil
	}
	roomToken, _ := s.rt.talkRoomForJob(jobID)
	if roomToken == "" {
		// No room on record (an import, a non-Talk job): administrators only.
		return false, nil
	}
	roomURL := strings.TrimRight(s.exapp.NextcloudURL, "/") + "/ocs/v2.php/apps/spreed/api/v4/room/" + url.PathEscape(roomToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, roomURL, nil)
	if err != nil {
		return false, err
	}
	s.exapp.setAppAPIOCSHeadersForUser(req, caller)
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("room check: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		// Not in the room, or the room is gone: not its moderator.
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("room check returned %d", resp.StatusCode)
	}
	var payload struct {
		OCS struct {
			Data struct {
				ParticipantType int `json:"participantType"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("decode room: %w", err)
	}
	t := payload.OCS.Data.ParticipantType
	return t == talkParticipantOwner || t == talkParticipantModerator, nil
}

func (s *annotationService) canRewriteSummary(ctx context.Context, caller, jobID string) bool {
	return s.summaryRewrite.allowed(ctx, caller, jobID, s.logf)
}

// readerSummaryState is GET/POST annotations/meetings/<id>/summary: what the
// meeting's summary is, what writing or replacing it would cost, and whether
// this reader may.
type readerSummaryState struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	HasSummary bool   `json:"hasSummary"`
	// CanWrite: no summary, and one can be written now.
	CanWrite bool `json:"canWrite"`
	// CanRegenerate: a summary, and this reader may replace it.
	CanRegenerate        bool   `json:"canRegenerate"`
	Calls                int    `json:"calls"`
	Words                int    `json:"words"`
	EstimatedInputTokens int    `json:"estimatedInputTokens"`
	Provider             string `json:"provider,omitempty"`
	Model                string `json:"model,omitempty"`
	External             bool   `json:"external"`
	// State: idle, writing (an attempt is queued or running), or failed (the
	// last one did; the meeting is as it was).
	State string `json:"state"`
}

const (
	readerSummaryIdle    = "idle"
	readerSummaryWriting = "writing"
	readerSummaryFailed  = "failed"
)

func (s *annotationService) routeSummary(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	switch r.Method {
	case http.MethodGet, http.MethodPost:
	default:
		writeMethodNotAllowed(w, "GET, POST")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), speakerEditsReadTimeout)
	defer cancel()
	opusName, ok := s.authorizedSpeakersRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	jobID := speakerJobID(opusName)
	job, err := s.rt.store.GetJob(ctx, jobID)
	if jobID == "" || errors.Is(err, sql.ErrNoRows) {
		// As for speaker edits: a recording with no job is a meeting this
		// surface does not know.
		s.answerFailure(w, r, "summary meeting="+meetingID, annotateNotFound(
			fmt.Errorf("meeting=%s has no operator job (served as 404)", meetingID)))
		return
	}
	if err != nil {
		s.logf("annotations: summary meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "summary unavailable")
		return
	}
	if r.Method == http.MethodPost {
		s.writeSummary(w, r, ctx, caller, meetingID, job)
		return
	}
	state, err := s.readerSummaryState(ctx, caller, job)
	if err != nil {
		s.logf("annotations: summary meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "summary unavailable")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *annotationService) readerSummaryState(ctx context.Context, caller string, job Job) (readerSummaryState, error) {
	plan, err := s.rt.summaryPlanFor(ctx, job)
	if err != nil {
		return readerSummaryState{}, err
	}
	state := readerSummaryState{
		Available: plan.Available, Reason: plan.Reason, HasSummary: plan.HasSummary,
		Calls: plan.Calls, Words: plan.Words, EstimatedInputTokens: plan.EstimatedInputTokens,
		Provider: plan.Provider, Model: plan.Model, External: plan.External,
		State: readerSummaryIdle,
	}
	state.CanWrite = plan.Available && !plan.HasSummary
	state.CanRegenerate = plan.Available && plan.HasSummary && s.canRewriteSummary(ctx, caller, job.ID)
	// The job's current attempt says whether a summary is being written, or
	// the last try failed. A failure's own words stay with the administrator:
	// they can name the endpoint.
	attempt, err := s.rt.store.GetJobAttempt(ctx, job.ID, job.CurrentAttemptNumber)
	if err == nil && attempt.TriggerKind == triggerKindSummarize {
		switch attempt.State {
		case "queued", "running":
			state.State = readerSummaryWriting
		case "failed":
			state.State = readerSummaryFailed
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	if state.State == readerSummaryWriting {
		state.CanWrite, state.CanRegenerate = false, false
	}
	return state, nil
}

// writeSummary queues a summarize attempt for a reader: a missing summary for
// anyone who can read the meeting, a replacement only for an administrator or
// a moderator of its room, and only when {"replace": true} says they know
// there is one.
func (s *annotationService) writeSummary(w http.ResponseWriter, r *http.Request, ctx context.Context, caller, meetingID string, job Job) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var request struct {
		Replace bool `json:"replace"`
	}
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			writeJSONError(w, http.StatusBadRequest, `the request body must be {"replace":true|false}`)
			return
		}
	}
	state, err := s.readerSummaryState(ctx, caller, job)
	if err != nil {
		s.logf("annotations: summary meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "summary unavailable")
		return
	}
	switch {
	case state.State == readerSummaryWriting:
		writeJSONError(w, http.StatusConflict, "busy")
		return
	case !state.Available:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "unavailable", "reason": state.Reason})
		return
	case state.HasSummary && !request.Replace:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "has-summary"})
		return
	case state.HasSummary && !state.CanRegenerate:
		writeJSONError(w, http.StatusForbidden, "only an administrator or a moderator of the room can replace this summary")
		return
	}
	now := s.rt.speakerNow()
	release, retryAfter, allowed := s.summaryWrites.reserve(caller, now)
	if !allowed {
		s.logf("annotations: summary meeting=%s refused for %s: too many (retry in %s)", meetingID, caller, retryAfter.Round(time.Second))
		writeSpeakerEditsRateLimited(w, retryAfter)
		return
	}
	// Whether a summary is there is decided again under the job's lock as the
	// attempt is queued: one published since this request's view must not be
	// replaced by someone who did not ask to, or may not.
	mayReplace := request.Replace && s.canRewriteSummary(ctx, caller, job.ID)
	attemptNumber, err := s.rt.queueSummarizeAttempt(ctx, job.ID, mayReplace, formatUTCString(now))
	if err != nil {
		release()
		if errors.Is(err, errSummaryExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "has-summary"})
			return
		}
		if errors.Is(err, ErrJobNotEligibleForRerun) {
			writeJSONError(w, http.StatusConflict, "busy")
			return
		}
		s.logf("annotations: summary meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "summary unavailable")
		return
	}
	s.rt.dispatchQueuedRefine(job.ID)
	s.logf("annotations: summary meeting=%s attempt=%d queued by %s replace=%t words=%d model=%q",
		meetingID, attemptNumber, caller, state.HasSummary, state.Words, state.Model)
	if job, err = s.rt.store.GetJob(ctx, job.ID); err == nil {
		state, err = s.readerSummaryState(ctx, caller, job)
	}
	if err != nil {
		s.logf("annotations: summary meeting=%s: %v", meetingID, err)
		writeJSONError(w, http.StatusInternalServerError, "summary unavailable")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// envSummaryWritesPerHour bounds how many summaries one person asks for an
// hour; each is a model call. 0 turns the limit off.
const (
	envSummaryWritesPerHour     = "CASSINI_SUMMARY_WRITES_PER_HOUR"
	defaultSummaryWritesPerHour = 10
)

func summaryWriteLimiterFromEnv() *speakerEditLimiter {
	return newSpeakerEditLimiter(envIntDefault(envSummaryWritesPerHour, defaultSummaryWritesPerHour), speakerEditsLimitWindow)
}
