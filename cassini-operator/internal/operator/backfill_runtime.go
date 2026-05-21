package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ErrJobNotEligibleForBackfill is returned when a job has no canonical meeting
// bundle or the bundle already publishes two or more transcripts side by side.
// The bulk driver treats this as "skip, move on" rather than fatal.
var ErrJobNotEligibleForBackfill = errors.New("job is not eligible for transcript backfill")

// minTranscriptsForBackfillSkip is the threshold above which a job is
// considered already-backfilled. A job with one transcript (the legacy v1
// single-inline case, or a v2 file with a single GPU pass) is eligible; two
// or more means the GPU and legacy CPU transcripts are already side by side.
const minTranscriptsForBackfillSkip = 2

// backfillJobResponse mirrors the rerunJobResponse contract so the control
// panel can treat both endpoints uniformly. ID is the job id, AttemptNumber
// is the newly-queued attempt.
type backfillJobResponse struct {
	ID            string `json:"id"`
	AttemptNumber int    `json:"attempt_number"`
}

// backfillEligibleResponse is the body returned by the eligibility endpoint.
// The control panel uses it to drive a serial backfill loop client-side: pull
// the list, POST /jobs/{id}/backfill-transcripts on each in turn, await
// terminal state via the events stream, then advance. Keeping the iteration
// in the UI rather than the operator avoids persisting a parallel queue
// state machine for a single-shot back-catalogue migration.
type backfillEligibleResponse struct {
	Jobs []backfillEligibleJob `json:"jobs"`
}

type backfillEligibleJob struct {
	ID                  string `json:"id"`
	CurrentAttemptCount int    `json:"transcripts"`
}

// handleBackfillTranscriptsJob queues a backfill rerun for a single job.
// Returns 409 when the job is ineligible (no canonical run, already has both
// transcripts), 404 when the job doesn't exist, 202 with the queued attempt
// number on success.
func (rt *Runtime) handleBackfillTranscriptsJob(w http.ResponseWriter, r *http.Request, id string) {
	job, err := rt.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get job: %v", err))
		return
	}
	if job.Stage != "done" || (job.State != "failed" && job.State != "succeeded") {
		writeJSONError(w, http.StatusConflict, ErrJobNotEligibleForBackfill.Error())
		return
	}
	eligible, reason, err := jobEligibleForBackfill(job)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("check backfill eligibility: %v", err))
		return
	}
	if !eligible {
		writeJSONError(w, http.StatusConflict, reason)
		return
	}

	queuedAt := nowUTCString()
	rerunJob, err := rt.store.QueueRerunAttemptWithKind(r.Context(), job, TriggerKindBackfillGPU, queuedAt)
	if err != nil {
		if errors.Is(err, ErrJobNotEligibleForRerun) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("queue backfill attempt: %v", err))
		return
	}
	if rerunJob.ArtifactRunPath == nil || strings.TrimSpace(*rerunJob.ArtifactRunPath) == "" {
		writeJSONError(w, http.StatusConflict, ErrJobNotEligibleForBackfill.Error())
		return
	}

	task := buildTask{
		JobID:           rerunJob.ID,
		AttemptNumber:   rerunJob.CurrentAttemptNumber,
		ArtifactRunPath: *rerunJob.ArtifactRunPath,
		TriggerKind:     TriggerKindBackfillGPU,
	}
	select {
	case rt.buildQueue <- task:
		rt.logger.Printf("backfill accepted id=%s attempt=%d run=%s", rerunJob.ID, rerunJob.CurrentAttemptNumber, task.ArtifactRunPath)
		writeJSON(w, http.StatusAccepted, backfillJobResponse{ID: rerunJob.ID, AttemptNumber: rerunJob.CurrentAttemptNumber})
	case <-rt.ctx.Done():
		if updateErr := rt.store.MarkBuildFailed(context.Background(), rerunJob.ID, "", "build queue stopped", nowUTCString()); updateErr != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("queue backfill attempt: %v", updateErr))
			return
		}
		writeJSONError(w, http.StatusServiceUnavailable, "build queue stopped")
	}
}

// jobEligibleForBackfill reports whether the given completed job has a
// canonical meeting bundle that currently publishes fewer than the threshold
// number of transcripts. Reads the recorder's artifact manifest.json — the
// .opus's OpusTags are the authoritative format marker, but the manifest's
// files.transcripts list is the same information one filesystem layer up and
// doesn't require Ogg parsing here in the operator.
func jobEligibleForBackfill(job Job) (bool, string, error) {
	meetingPath := strings.TrimSpace(stringFromPtr(job.ArtifactMeetingPath))
	if meetingPath == "" {
		return false, "job has no canonical meeting bundle", nil
	}
	count, err := countPublishedTranscripts(meetingPath)
	if err != nil {
		return false, "", err
	}
	if count >= minTranscriptsForBackfillSkip {
		return false, fmt.Sprintf("meeting already publishes %d transcripts", count), nil
	}
	return true, "", nil
}

// countPublishedTranscripts inspects the recorder's artifact manifest inside
// the meeting bundle and reports how many transcripts the published .opus
// carries. A bundle without a manifest, or with malformed JSON, is treated as
// eligible (count 0) — failing closed on a clearly-broken bundle would block
// the user's main use case (back-catalogue) for a recoverable read error.
func countPublishedTranscripts(meetingPath string) (int, error) {
	manifestPath := filepath.Join(meetingPath, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read meeting artifact manifest: %w", err)
	}
	var shape struct {
		Files struct {
			Transcripts []struct {
				ID string `json:"id"`
			} `json:"transcripts,omitempty"`
			Transcript string `json:"transcript,omitempty"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		// Treat as eligible: an unreadable manifest doesn't tell us the
		// transcript is already there. The recorder rejects malformed input
		// during the rerun itself, so we won't silently corrupt anything.
		return 0, nil
	}
	if n := len(shape.Files.Transcripts); n > 0 {
		return n, nil
	}
	if strings.TrimSpace(shape.Files.Transcript) != "" {
		return 1, nil
	}
	return 0, nil
}

func stringFromPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// handleBackfillEligible lists completed jobs whose canonical meeting bundle
// currently publishes fewer than the threshold number of transcripts. The
// response is ordered by job id so the control panel's iteration is stable
// across polls. Jobs with broken or missing artifact manifests are reported
// as eligible (count 0) — see countPublishedTranscripts for the rationale.
func (rt *Runtime) handleBackfillEligible(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	jobs, err := rt.store.ListJobs(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list jobs: %v", err))
		return
	}
	out := make([]backfillEligibleJob, 0)
	for _, job := range jobs {
		if job.Stage != "done" || (job.State != "succeeded" && job.State != "failed") {
			continue
		}
		meetingPath := strings.TrimSpace(stringFromPtr(job.ArtifactMeetingPath))
		if meetingPath == "" {
			continue
		}
		count, err := countPublishedTranscripts(meetingPath)
		if err != nil {
			rt.logger.Printf("backfill eligibility: job=%s count error: %v", job.ID, err)
			continue
		}
		if count >= minTranscriptsForBackfillSkip {
			continue
		}
		out = append(out, backfillEligibleJob{ID: job.ID, CurrentAttemptCount: count})
	}
	writeJSON(w, http.StatusOK, backfillEligibleResponse{Jobs: out})
}
