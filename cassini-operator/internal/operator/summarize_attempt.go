package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// A summarize attempt writes a summary into a meeting the job already
// published, without transcribing again (D-814, D-712).
//
// It is a sibling of the refine attempt: it copies the published meeting
// bundle, runs `cassini meetings summarize` on the copy, and the ordinary seal
// and publish carry the result into the delivered file, marks and shares
// included. The summary has to go into the bundle, not only the .opus: speaker
// edits and republishes seal again from the bundle. Nothing reads the
// capture, so retention removing it does not stop a summary being written;
// only a job whose meeting is deleted after processing has nothing to write
// into.
//
// It always runs with --force. Whether replacing a summary is allowed is
// decided when the attempt is queued, by whoever asked and what they
// confirmed, not by the worker.

const triggerKindSummarize = "summarize"

// summaryPlan is what writing (or rewriting) one meeting's summary would do
// and cost, shown before anyone confirms it: one model call, the words it
// reads, roughly how many input tokens that is, and where it goes. External
// says the endpoint is not on this network, so the call may be billed.
type summaryPlan struct {
	Available            bool   `json:"available"`
	Reason               string `json:"reason,omitempty"`
	HasSummary           bool   `json:"has_summary"`
	Calls                int    `json:"calls"`
	Words                int    `json:"words"`
	EstimatedInputTokens int    `json:"estimated_input_tokens"`
	Provider             string `json:"provider,omitempty"`
	Model                string `json:"model,omitempty"`
	External             bool   `json:"external"`
}

// Why a summary cannot be written now.
const (
	summaryUnavailableNotPublished = "not_published"
	summaryUnavailableNoTranscript = "no_transcript"
	summaryUnavailableOff          = "summaries_off"
	summaryUnavailableNoCopy       = "no_local_copy"
	summaryUnavailableBusy         = "busy"
)

// summarizeCLIReport is one entry of `cassini meetings summarize --json`.
type summarizeCLIReport struct {
	Path                 string `json:"path"`
	Status               string `json:"status"`
	Reason               string `json:"reason"`
	Model                string `json:"model"`
	Words                int    `json:"words"`
	EstimatedInputTokens int    `json:"estimatedInputTokens"`
	Error                string `json:"error"`
}

// summaryEndpoint is the provider and model the summary step would call, and
// whether it is on at all.
func (rt *Runtime) summaryEndpoint() (provider LLMProvider, model string, on bool) {
	settings := rt.currentLLMSettings()
	provider, on = settings.provider(settings.Summary)
	model = strings.TrimSpace(settings.Summary.Model)
	if model == "" {
		model = strings.TrimSpace(provider.Model)
	}
	return provider, model, on
}

// summaryPlanFor says what a summarize attempt on job would do and cost.
func (rt *Runtime) summaryPlanFor(ctx context.Context, job Job) (summaryPlan, error) {
	plan := summaryPlan{Calls: 1}
	provider, model, on := rt.summaryEndpoint()
	plan.Provider, plan.Model, plan.External = provider.Name, model, on && !localEndpoint(provider.BaseURL)

	source, published, err := rt.publishedSpeakerMeetingPath(ctx, job.ID)
	if err != nil {
		return plan, err
	}
	if _, err := os.Stat(filepath.Join(source, "cassini.json")); err != nil {
		// Published, but the operator's own copy of the meeting, which the
		// summary is written into, is gone with the recording's media.
		plan.Reason = summaryUnavailableNotPublished
		if published > 0 || deletesSourceMedia(job) {
			plan.Reason = summaryUnavailableNoCopy
		}
		return plan, nil
	}
	outcomes, err := readBundleStepOutcomes(source)
	if err != nil {
		return plan, err
	}
	plan.HasSummary = outcomes.SummaryStatus == "completed" && pathExists(filepath.Join(source, "summary.md"))
	reports, err := rt.estimateSummaries(ctx, []string{source})
	if err != nil {
		return plan, err
	}
	report := reports[0]
	plan.Words, plan.EstimatedInputTokens = report.Words, report.EstimatedInputTokens
	switch {
	case report.Status != "estimated":
		plan.Reason = summaryUnavailableNoTranscript
	case !on:
		plan.Reason = summaryUnavailableOff
	case job.ArtifactRunPath == nil || strings.TrimSpace(*job.ArtifactRunPath) == "" || deletesSourceMedia(job):
		plan.Reason = summaryUnavailableNoCopy
	case job.State != "succeeded" && job.State != "failed" && job.State != "interrupted" && job.State != "blocked":
		plan.Reason = summaryUnavailableBusy
	}
	plan.Available = plan.Reason == ""
	return plan, nil
}

// estimateSummaries sizes the summaries of the given bundles in one CLI call,
// with --force so a meeting that has one is sized too. No model is called.
func (rt *Runtime) estimateSummaries(ctx context.Context, bundles []string) ([]summarizeCLIReport, error) {
	out, err := rt.runMeetingsCLI(ctx, os.Environ(), io.Discard, append([]string{"summarize", "--estimate", "--force", "--json"}, bundles...)...)
	if err != nil {
		return nil, err
	}
	var reports []summarizeCLIReport
	if err := json.Unmarshal(out, &reports); err != nil {
		return nil, fmt.Errorf("parse summary estimate: %w", err)
	}
	if len(reports) != len(bundles) {
		return nil, fmt.Errorf("summary estimate: %d reports for %d meetings", len(reports), len(bundles))
	}
	return reports, nil
}

// localEndpoint reports whether an LLM endpoint is on this host or network:
// a loopback or private address, or a name with no domain (a container on the
// same Docker network). Anything else may be a billed service.
func localEndpoint(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return !strings.Contains(host, ".")
}

// errSummaryExists refuses a request that did not ask to replace a summary,
// for a meeting that has one by the time it would be queued.
var errSummaryExists = errors.New("the meeting has a summary")

// queueSummarizeAttempt queues a summarize attempt for jobID. Whether the
// meeting has a summary is decided again here, under the job's artifact lock
// that every build, seal and publish holds: a plan read before it can be stale
// (another request's summary may have been published since), and a request
// that did not ask to replace one must never replace one.
func (rt *Runtime) queueSummarizeAttempt(ctx context.Context, jobID string, replace bool, queuedAt string) (int, error) {
	unlock := rt.store.lockArtifacts(jobID)
	defer unlock()
	if !replace {
		has, err := rt.publishedHasSummary(ctx, jobID)
		if err != nil {
			return 0, err
		}
		if has {
			return 0, errSummaryExists
		}
	}
	return rt.store.queueSummarizeAttemptLocked(ctx, jobID, queuedAt)
}

// publishedHasSummary says whether the meeting readers have carries a summary.
func (rt *Runtime) publishedHasSummary(ctx context.Context, jobID string) (bool, error) {
	source, _, err := rt.publishedSpeakerMeetingPath(ctx, jobID)
	if err != nil {
		return false, err
	}
	outcomes, err := readBundleStepOutcomes(source)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return outcomes.SummaryStatus == "completed" && pathExists(filepath.Join(source, "summary.md")), nil
}

// QueueSummarizeAttempt queues a summarize attempt for jobID: no archive or
// retention operation pending and the job at rest, as for a rerun, but the
// capture may be gone, since nothing is transcribed. It does not look at the
// summary; queueSummarizeAttempt does, and handlers use that.
func (s *Store) QueueSummarizeAttempt(ctx context.Context, jobID, queuedAt string) (int, error) {
	unlock := s.lockArtifacts(jobID)
	defer unlock()
	return s.queueSummarizeAttemptLocked(ctx, jobID, queuedAt)
}

// queueSummarizeAttemptLocked is QueueSummarizeAttempt for a caller that
// holds the job's artifact lock.
func (s *Store) queueSummarizeAttemptLocked(ctx context.Context, jobID, queuedAt string) (int, error) {
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return 0, err
	}
	if deletesSourceMedia(job) {
		return 0, fmt.Errorf("%w: the meeting is deleted after processing", ErrJobNotEligibleForRerun)
	}
	// An expired capture does not matter: the summary is written into the
	// published meeting, and the seal packs that meeting's own audio.
	if pending, _ := s.artifactAdmission(ctx, jobID); pending {
		return 0, ErrJobNotEligibleForRerun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin summarize attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attemptNumber, err := queueAttemptTx(ctx, tx, jobID, triggerKindSummarize, nil, queuedAt)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit summarize attempt: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", jobID, attemptNumber)
	return attemptNumber, nil
}

// executeSummarizeCLI builds a summarize attempt's meeting: a fresh copy of
// the job's published meeting with a summary written into it. Nothing else in
// the copy changes, so the seal packs the same audio and every mark stays.
func (rt *Runtime) executeSummarizeCLI(ctx context.Context, task buildTask) (string, error) {
	meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
	if err := os.RemoveAll(meetingPath); err != nil {
		return meetingPath, fmt.Errorf("clear summarize meeting dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(meetingPath), 0o755); err != nil {
		return meetingPath, fmt.Errorf("create meeting parent dir: %w", err)
	}
	logPath, logFile, err := openAttemptLogFile(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber, "build")
	if err != nil {
		return meetingPath, err
	}
	defer logFile.Close()
	if err := rt.store.SetAttemptStageLogPath(context.Background(), task.JobID, task.AttemptNumber, "build", logPath); err != nil {
		return meetingPath, err
	}
	// The meeting readers have. The worker holds the job's artifact lock, so
	// no promotion or prune of this job runs while it is copied.
	source, _, err := rt.publishedSpeakerMeetingPath(ctx, task.JobID)
	if err != nil {
		return meetingPath, err
	}
	if _, err := os.Stat(filepath.Join(source, "cassini.json")); err != nil {
		return meetingPath, fmt.Errorf("a summary is written into the job's published meeting, and there is none: %w", err)
	}
	if unpublished, err := rt.speakerBuildUnpublished(ctx, task.JobID, readSpeakerMeeting(source).builtBy); err != nil {
		return meetingPath, err
	} else if unpublished {
		return meetingPath, fmt.Errorf("%s is a rebuild that was never published; a summary is written only into the published meeting", source)
	}
	fmt.Fprintf(logFile, "summarize: writing a summary into a copy of %s\n", source)
	if err := copyDirectory(source, meetingPath); err != nil {
		return meetingPath, fmt.Errorf("copy current meeting: %w", err)
	}
	out, err := rt.runMeetingsCLI(ctx, rt.childEnv(), logFile, "summarize", "--force", "--json", meetingPath)
	if err != nil {
		return meetingPath, err
	}
	var reports []summarizeCLIReport
	if err := json.Unmarshal(out, &reports); err != nil || len(reports) != 1 {
		return meetingPath, fmt.Errorf("parse summarize report: %v (%q)", err, firstLine(string(out)))
	}
	if report := reports[0]; report.Status != "summarized" {
		why := report.Error
		if why == "" {
			why = report.Reason
		}
		return meetingPath, fmt.Errorf("no summary written: %s", why)
	}
	return meetingPath, nil
}

// summarizeResult hands a failed summarize attempt to the build worker with
// no bundle path, so it reports the reason and not the copy's manifest. A
// recording that started is not a failure: the attempt yields and runs again.
func summarizeResult(ctx context.Context, builtPath string, err error) (string, error) {
	if err == nil || ctx.Err() != nil || transientResourceError(err) {
		return builtPath, err
	}
	return "", err
}

// runMeetingsCLI runs `cassini meetings <args>` in its own process group and
// returns its stdout; a failure carries the tail of its stderr.
func (rt *Runtime) runMeetingsCLI(ctx context.Context, env []string, logFile io.Writer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, append([]string{"meetings"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, logFile)
	cmd.Stderr = io.MultiWriter(&stderr, writerOrDiscard(rt.stderr), logFile)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
		// summarize exits 1 when a file failed and still prints its report.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && bytes.HasPrefix(bytes.TrimSpace(stdout.Bytes()), []byte("[")) {
			return stdout.Bytes(), nil
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxSpeakersCLIStderr {
			msg = msg[len(msg)-maxSpeakersCLIStderr:]
		}
		return nil, fmt.Errorf("cassini meetings %s: %w: %s", args[0], err, msg)
	}
	return stdout.Bytes(), nil
}

// handleJobSummary serves /jobs/{id}/summary: GET says what writing the
// summary would do and cost, POST queues it. Replacing a summary the meeting
// already has needs {"replace": true}, so a request made against a stale view
// cannot overwrite one it did not know about.
func (rt *Runtime) handleJobSummary(w http.ResponseWriter, r *http.Request, id string) {
	job, err := rt.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("get job: %v", err))
		return
	}
	plan, err := rt.summaryPlanFor(r.Context(), job)
	if err != nil {
		rt.logger.Printf("summary plan failed id=%s: %v", id, err)
		writeJSONError(w, http.StatusInternalServerError, "could not tell what a summary would cost")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, plan)
		return
	case http.MethodPost:
	default:
		writeMethodNotAllowed(w, "GET, POST")
		return
	}
	var body struct {
		Replace bool `json:"replace"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !plan.Available {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "unavailable", "reason": plan.Reason})
		return
	}
	if plan.HasSummary && !body.Replace {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "has_summary"})
		return
	}
	attemptNumber, err := rt.queueSummarizeAttempt(r.Context(), id, body.Replace, nowUTCString())
	if err != nil {
		if errors.Is(err, errSummaryExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "has_summary"})
			return
		}
		if errors.Is(err, ErrJobNotEligibleForRerun) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "unavailable", "reason": summaryUnavailableBusy})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("queue summarize attempt: %v", err))
		return
	}
	rt.logger.Printf("summary queued id=%s attempt=%d replace=%t words=%d model=%q", id, attemptNumber, plan.HasSummary, plan.Words, plan.Model)
	rt.dispatchQueuedRefine(id)
	writeJSON(w, http.StatusAccepted, rerunJobResponse{ID: id, AttemptNumber: attemptNumber})
}

// missingSummary is one published meeting without a summary, sized.
type missingSummary struct {
	JobID                string `json:"job_id"`
	RoomName             string `json:"room_name,omitempty"`
	Words                int    `json:"words"`
	EstimatedInputTokens int    `json:"estimated_input_tokens"`
}

// missingSummaries is GET /summaries/missing: every published meeting whose
// transcript has words and which has no summary, with the cost of writing
// them all. What the backfill would do, before it does anything (D-712).
func (rt *Runtime) missingSummariesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	jobs, err := rt.store.ListJobs(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("list jobs: %v", err))
		return
	}
	var bundles []string
	var candidates []Job
	for _, job := range jobs {
		source, _, err := rt.publishedSpeakerMeetingPath(r.Context(), job.ID)
		if err != nil {
			rt.logger.Printf("missing summaries: published meeting of %s: %v", job.ID, err)
			continue
		}
		outcomes, err := readBundleStepOutcomes(source)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				rt.logger.Printf("missing summaries: %s: %v", job.ID, err)
			}
			continue
		}
		if outcomes.SummaryStatus == "completed" || (outcomes.TranscriptionStatus != "" && outcomes.TranscriptionStatus != "completed") {
			continue
		}
		bundles = append(bundles, source)
		candidates = append(candidates, job)
	}
	provider, model, on := rt.summaryEndpoint()
	out := struct {
		Meetings                  []missingSummary `json:"meetings"`
		Calls                     int              `json:"calls"`
		TotalWords                int              `json:"total_words"`
		TotalEstimatedInputTokens int              `json:"total_estimated_input_tokens"`
		SummariesOn               bool             `json:"summaries_on"`
		Provider                  string           `json:"provider,omitempty"`
		Model                     string           `json:"model,omitempty"`
		External                  bool             `json:"external"`
	}{Meetings: []missingSummary{}, SummariesOn: on, Provider: provider.Name, Model: model, External: on && !localEndpoint(provider.BaseURL)}
	if len(bundles) > 0 {
		reports, err := rt.estimateSummaries(r.Context(), bundles)
		if err != nil {
			rt.logger.Printf("missing summaries: estimate: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "could not size the missing summaries")
			return
		}
		for i, report := range reports {
			if report.Status != "estimated" {
				continue
			}
			job := candidates[i]
			out.Meetings = append(out.Meetings, missingSummary{
				JobID: job.ID, RoomName: stringOrEmpty(job.RoomName),
				Words: report.Words, EstimatedInputTokens: report.EstimatedInputTokens,
			})
			out.TotalWords += report.Words
			out.TotalEstimatedInputTokens += report.EstimatedInputTokens
		}
	}
	out.Calls = len(out.Meetings)
	writeJSON(w, http.StatusOK, out)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
