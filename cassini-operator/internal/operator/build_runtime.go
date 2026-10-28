package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBuildWorkerCount        = 1
	defaultBuildResourceRetryDelay = 15 * time.Second
	maxBuildResourceRetryDelay     = 15 * time.Minute
	// Sixteen bounded deferrals cover about 2h46m, so a normal hour-scale
	// neighbor workload on a shared GPU does not strand a valid recording.
	defaultMaxBuildResourceDeferrals = 16
)

type buildTask struct {
	JobID           string
	AttemptNumber   int
	ArtifactRunPath string
	DeferralCount   int
}

func (rt *Runtime) startBuildWorkers() {
	rt.workerWG.Add(rt.cfg.MaxBuildWorkers)
	for i := 0; i < rt.cfg.MaxBuildWorkers; i++ {
		go rt.buildWorker(i + 1)
	}
}

func (rt *Runtime) buildWorker(index int) {
	defer rt.workerWG.Done()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case task := <-rt.buildQueue:
			rt.runBuildJob(task, index)
		}
	}
}

func (rt *Runtime) runBuildJob(task buildTask, workerIndex int) {
	unlock := rt.store.lockArtifacts(task.JobID)
	defer unlock()
	defer rt.cleanupMediaAfterStage(task.JobID)
	// MaxBuildWorkers controls queue consumers, not simultaneous GPU inference.
	// Hold one process-wide admission lock across claim, resource checks, and the
	// complete build so two workers cannot both observe the same RAM/VRAM as free.
	rt.buildExecutionMu.Lock()
	defer rt.buildExecutionMu.Unlock()
	if rt.ctx.Err() != nil {
		return
	}

	buildCtx, release, admissionErr := rt.beginBackgroundBuild()
	if admissionErr != nil {
		return
	}
	defer release()
	startedAt := nowUTCString()
	claimed, err := rt.store.ClaimBuildRunning(context.Background(), task, startedAt)
	if err != nil {
		rt.logger.Printf("build start update failed id=%s worker=%d: %v", task.JobID, workerIndex, err)
		return
	}
	if !claimed {
		// Duplicate delivery (direct enqueue plus a requeue-dispatcher
		// re-scan) or the job state changed since queueing; another worker
		// owns it (D-367).
		rt.logger.Printf("build claim skipped id=%s attempt=%d worker=%d: job is not build/queued", task.JobID, task.AttemptNumber, workerIndex)
		return
	}
	rt.logger.Printf("build started id=%s attempt=%d worker=%d run=%s", task.JobID, task.AttemptNumber, workerIndex, task.ArtifactRunPath)

	builtMeetingPath, err := rt.buildJobFn(buildCtx, task)
	finishedAt := nowUTCString()
	if err != nil && errors.Is(context.Cause(buildCtx), errRecordingPriority) {
		// Only discard this interrupted attempt's derived output; the canonical
		// capture and all published artifacts are untouched. The child process
		// group has exited before buildJobFn returns.
		if cleanErr := os.RemoveAll(attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)); cleanErr != nil {
			rt.logger.Printf("build priority cleanup failed id=%s: %v", task.JobID, cleanErr)
			_ = rt.store.MarkBuildFailed(context.Background(), task.JobID, "", cleanErr.Error(), finishedAt)
			return
		}
		retry := time.Now().UTC()
		queued, queueErr := rt.store.MarkBuildDeferred(context.Background(), task, task.DeferralCount, errRecordingPriority.Error(), finishedAt, formatUTCString(retry))
		if queueErr != nil {
			rt.logger.Printf("build priority requeue failed id=%s: %v", task.JobID, queueErr)
			return
		}
		if queued {
			rt.logger.Printf("build yielded for recording id=%s", task.JobID)
			rt.scheduleDeferredBuild(task, retry)
		}
		return
	}
	if err != nil {
		var unavailable *resourceUnavailableError
		if errors.As(err, &unavailable) {
			maxDeferrals := rt.buildResourceDeferralLimit()
			nextDeferral := task.DeferralCount + 1
			ceiling := unavailable.permanent || nextDeferral > maxDeferrals
			var job Job
			if ceiling {
				var getErr error
				job, getErr = rt.store.GetJob(context.Background(), task.JobID)
				if getErr != nil {
					rt.logger.Printf("read build policy: %v", getErr)
					return
				}
				// Failing a delete-after-processing job deletes its source media.
				// A speech model that is being installed, that replaces a
				// retired one, or that the job's saved settings select and is
				// not installed is worth waiting for past the ceiling: losing
				// the recording without a transcript is not.
				if !unavailable.permanent && unavailable.awaitsModel && deletesSourceMedia(job) {
					ceiling = false
				}
			}
			if ceiling {
				if deletesSourceMedia(job) {
					if err := rt.store.MarkBuildFailed(context.Background(), task.JobID, builtMeetingPath, unavailable.Error(), finishedAt); err != nil {
						rt.logger.Printf("terminal build failure: %v", err)
					}
					return
				}
				blocked, blockErr := rt.store.MarkBuildBlocked(
					context.Background(), task, strings.TrimSpace(unavailable.Error()), finishedAt,
				)
				if blockErr != nil {
					rt.logger.Printf("build resource block update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, blockErr)
					return
				}
				if !blocked {
					rt.logger.Printf("build resource block skipped id=%s attempt=%d worker=%d: job is no longer build/running", task.JobID, task.AttemptNumber, workerIndex)
					return
				}
				reason := "permanent resource condition"
				if !unavailable.permanent {
					reason = fmt.Sprintf("retry ceiling reached after %d deferrals", task.DeferralCount)
				}
				rt.logger.Printf("build blocked id=%s attempt=%d worker=%d (%s): %v", task.JobID, task.AttemptNumber, workerIndex, reason, unavailable)
				return
			}
			delay := exponentialBuildRetryDelay(rt.buildResourceRetryDelay, nextDeferral)
			retryNotBefore := time.Now().UTC().Add(delay)
			deferred, deferErr := rt.store.MarkBuildDeferred(
				context.Background(), task, nextDeferral, strings.TrimSpace(unavailable.Error()),
				finishedAt, formatUTCString(retryNotBefore),
			)
			if deferErr != nil {
				rt.logger.Printf("build resource defer update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, deferErr)
				return
			}
			if !deferred {
				rt.logger.Printf("build resource defer skipped id=%s attempt=%d worker=%d: job is no longer build/running", task.JobID, task.AttemptNumber, workerIndex)
				return
			}
			task.DeferralCount = nextDeferral
			rt.logger.Printf("build deferred id=%s attempt=%d worker=%d count=%d retry_in=%s: %v", task.JobID, task.AttemptNumber, workerIndex, nextDeferral, delay, unavailable)
			rt.scheduleDeferredBuild(task, retryNotBefore)
			return
		}
		detail := rt.extractBuildFailureDetail(builtMeetingPath, err)
		rt.logger.Printf("build failed id=%s attempt=%d worker=%d: %s", task.JobID, task.AttemptNumber, workerIndex, detail)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), task.JobID, builtMeetingPath, detail, finishedAt); updateErr != nil {
			rt.logger.Printf("build fail update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	// Stamp the Talk room into the ATTEMPT bundle, before it is promoted. The
	// seal that follows packs this bundle and the promoted copy inherits the
	// stamp, so one write names both — and it reaches the `.opus` the viewer
	// reads rather than only the bundle nobody publishes any more (D-462).
	//
	// The room's name is also the meeting title, and its token is what the
	// published room id is derived from (D-622). The job and attempt go on the
	// same stamp (D-640), and unconditionally: a bundle always has a job, even
	// when it has no room, and it is the only lineage a `.opus` published
	// through `cassini publish <bundle>` would otherwise carry.
	// Best-effort: a failed stamp costs the room, never the meeting.
	roomToken, roomName := rt.talkRoomForJob(task.JobID)
	// The title and the room name part ways for one-to-one rooms: Talk names
	// those after the counterpart, so the default title becomes both
	// participants instead ("Alex & Chris", D-862). The room name field keeps
	// the room's own display name either way.
	meetingTitle := rt.defaultMeetingTitle(task.JobID, roomName)
	if err := SetMeetingBundleRoom(builtMeetingPath, meetingTitle, roomToken, roomName, task.JobID, task.AttemptNumber); err != nil {
		rt.logger.Printf("meeting room stamp failed id=%s meeting=%s: %v (viewer falls back to Untitled meeting; the meeting will carry no room)", task.JobID, builtMeetingPath, err)
	}
	// What the transcription and summary steps did, onto the attempt, so the
	// run can say it published without either (D-854, D-814). Best-effort and
	// logged: a missing record costs the Operator a chip, never the meeting.
	if outcomes, err := readBundleStepOutcomes(builtMeetingPath); err != nil {
		rt.logger.Printf("step outcomes unreadable id=%s attempt=%d meeting=%s: %v", task.JobID, task.AttemptNumber, builtMeetingPath, err)
	} else if err := rt.store.RecordAttemptStepOutcomes(context.Background(), task.JobID, task.AttemptNumber, outcomes); err != nil {
		rt.logger.Printf("step outcomes update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, err)
	}
	// Current output denotes the last successful publication, never a build.
	canonicalMeetingPath := builtMeetingPath
	// Hand off to the seal worker, not to publish. Sealing the portable `.opus`
	// used to be a detached goroutine started right here, after publish was
	// already queued: best-effort, unordered across reruns, and invisible when
	// it failed. It is a stage now, and its success is what makes the job
	// publishable (D-583). The hand-off is still non-blocking, so the reason it
	// was detached in the first place — a single build worker starved by an
	// ffmpeg pack — still does not apply.
	if err := rt.enqueueSealJobNonBlocking(task.JobID, task.AttemptNumber, canonicalMeetingPath, builtMeetingPath, finishedAt); err != nil {
		rt.logger.Printf("seal queue update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, err)
		if updateErr := rt.store.MarkSealFailed(context.Background(), task.JobID, "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("seal queue failure update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	rt.logger.Printf("build succeeded id=%s attempt=%d worker=%d attempt_meeting=%s canonical_meeting=%s seal_queued_at=%s", task.JobID, task.AttemptNumber, workerIndex, builtMeetingPath, canonicalMeetingPath, finishedAt)
}

func exponentialBuildRetryDelay(base time.Duration, deferralCount int) time.Duration {
	if base <= 0 {
		base = defaultBuildResourceRetryDelay
	}
	if deferralCount < 1 {
		deferralCount = 1
	}
	delay := base
	for count := 1; count < deferralCount && delay < maxBuildResourceRetryDelay; count++ {
		if delay > maxBuildResourceRetryDelay/2 {
			return maxBuildResourceRetryDelay
		}
		delay *= 2
	}
	if delay > maxBuildResourceRetryDelay {
		return maxBuildResourceRetryDelay
	}
	return delay
}

// scheduleDeferredBuild waits for the exponentially calculated retry time
// before redelivery. The durable row remains build/queued throughout. Waiting
// in a goroutine avoids occupying the sole build worker, and the blocking
// channel handoff (bounded by shutdown) guarantees delivery even when the
// requeue dispatcher's duplicate tracker still remembers the original handoff.
func (rt *Runtime) scheduleDeferredBuild(task buildTask, retryNotBefore time.Time) {
	go func() {
		delay := time.Until(retryNotBefore)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-rt.ctx.Done():
			return
		case <-timer.C:
		}
		// Prefer shutdown over delivery when both become ready together.
		if rt.ctx.Err() != nil {
			return
		}
		select {
		case <-rt.ctx.Done():
		case rt.buildQueue <- task:
			rt.logger.Printf("build resource retry queued id=%s attempt=%d", task.JobID, task.AttemptNumber)
		}
	}()
}

// enqueueBuildJob durably marks the job build/queued and hands it to a worker
// without ever blocking the caller: the DB row is the source of truth and the
// requeue dispatcher re-delivers any task the channel could not accept (full
// queue) or never saw (operator restart) (D-367).
func (rt *Runtime) enqueueBuildJob(jobID string, attemptNumber int, jobArtifactRunPath, attemptArtifactRunPath, queuedAt string) error {
	if err := rt.store.MarkBuildQueued(context.Background(), jobID, jobArtifactRunPath, attemptArtifactRunPath, queuedAt); err != nil {
		return err
	}
	task := buildTask{JobID: jobID, AttemptNumber: attemptNumber, ArtifactRunPath: jobArtifactRunPath}
	select {
	case rt.buildQueue <- task:
	default:
		rt.logger.Printf("build queue full id=%s attempt=%d: durably queued for the requeue dispatcher", jobID, attemptNumber)
		rt.kickRequeueScan()
	}
	return nil
}

func (rt *Runtime) buildResourceDeferralLimit() int {
	if rt.maxBuildResourceDeferrals > 0 {
		return rt.maxBuildResourceDeferrals
	}
	return defaultMaxBuildResourceDeferrals
}

// transientResourceError reports whether waiting may clear err.
func transientResourceError(err error) bool {
	var unavailable *resourceUnavailableError
	return errors.As(err, &unavailable) && !unavailable.permanent
}

// admitTranscription resolves the device and the selected model for one build,
// running the model's runtime check first when a restart or upgrade invalidated
// it. It returns the settings the build runs with: a pin to a retired model
// (the global settings before their migration, or the snapshot a
// delete-after-processing job saved at admission) resolves to the model that
// replaces it on that tier and device, at its revision. A non-permanent
// *resourceUnavailableError means waiting may help; any other error means this
// build cannot transcribe.
//
// dispose says the job deletes its source media once it is processed. Its
// build cannot publish audio without a transcript, and failing it deletes the
// recording, so a selected model that is not installed is waited for (and
// installed, see awaitModelInstall) like the replacement of a retired pin,
// instead of being reported as unavailable.
//
// Wherever the selected model would make the build wait or go without a
// transcript, the retired model it replaces stands in when its files are
// still installed and pass their runtime check here (see admitStandIn): the
// returned settings then name that model, with StandInFor set. Everything
// else happens as without one, so the selected model's install is still
// queued or left running. The caller holds buildExecutionMu for the whole
// build, which keeps an install job's probe from removing the stand-in's
// files meanwhile.
func (rt *Runtime) admitTranscription(ctx context.Context, settings STTSettings, limits resourceLimits, dispose bool) (string, STTSettings, error) {
	device, err := resolveDeviceForSettings(settings)
	if err != nil {
		return "", settings, err
	}
	if settings.ActiveModel == "" || settings.ActiveRevision == "" {
		return "", settings, errors.New("no speech model is selected")
	}
	inventory, err := rt.modelInventory(ctx, device)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// A slow host, not a broken install.
			return "", settings, &resourceUnavailableError{resource: "speech model inventory", detail: err.Error()}
		}
		return "", settings, err
	}
	selected, err := findModel(inventory, settings.ActiveModel, settings.ActiveRevision)
	retiredPin := ""
	if err != nil {
		replacement, retired, replErr := retiredReplacement(inventory, settings.ActiveModel, settings.Quality, device)
		if replErr != nil {
			return "", settings, replErr
		}
		if !retired {
			return "", settings, err
		}
		retiredPin = settings.ActiveModel
		rt.logger.Printf("resource governor: speech model %s is retired; this build uses %s (%s), which replaces it for quality=%s on %s", retiredPin, replacement.ID, replacement.Revision, normalizeQuality(settings.Quality), device)
		settings.ActiveModel, settings.ActiveRevision = replacement.ID, replacement.Revision
		selected = replacement
	}
	// standIn is what admission returns where the selected model cannot run:
	// the retired model it replaces, when that can, or else cause unchanged.
	standIn := func(cause error, reason string) (string, STTSettings, error) {
		retired, ok := rt.admitStandIn(ctx, inventory, selected, device, limits, retiredPin)
		if !ok {
			return "", settings, cause
		}
		detail := cause.Error()
		var wait *resourceUnavailableError
		if errors.As(cause, &wait) {
			detail = wait.detail
		}
		if reason == standInReasonRuntimeCheckFailed {
			detail = "failed its runtime check: " + detail
		}
		rt.logger.Printf("resource governor: speech model %s is not ready (%s: %s); this build transcribes with retired %s (%s), which it replaces, as a stand-in on %s", selected.ID, reason, detail, retired.ID, retired.Revision, device)
		settings.ActiveModel, settings.ActiveRevision = retired.ID, retired.Revision
		settings.StandInFor, settings.StandInReason, settings.StandInDetail = selected.ID, reason, detail
		return device, settings, nil
	}
	if !selected.Installed {
		job, hasJob := rt.modelJobFor(ctx, selected.ID, selected.Revision, device)
		if hasJob && modelJobRunning(job) {
			// Being installed (after an upgrade replaced the model, typically):
			// waiting is what makes this transcript possible.
			return standIn(&resourceUnavailableError{resource: "speech model " + selected.ID, detail: "being installed (" + modelJobProgressText(job) + ")", awaitsModel: true}, standInReasonInstalling)
		}
		if hasJob && job.State == "ready" {
			// The install may have finished after the inventory was read.
			rt.invalidateModelInventory()
			if fresh, err := rt.modelInventory(ctx, device); err == nil {
				inventory = fresh
				if m, err := findModel(fresh, selected.ID, selected.Revision); err == nil {
					selected = m
				}
			}
		}
	}
	if !selected.Installed {
		if retiredPin != "" || dispose {
			// Nothing else installs the replacement when the global settings
			// have moved on since this job saved its pin, and nothing may
			// install a model that is missing for any other reason. Neither is
			// this recording's fault: wait for the model, never fail it.
			wait := rt.awaitModelInstall(ctx, selected, device, retiredPin)
			return standIn(wait, rt.notInstalledReason(ctx, selected, device))
		}
		return standIn(fmt.Errorf("speech model %s %s is not installed", selected.ID, selected.Revision), rt.notInstalledReason(ctx, selected, device))
	}
	if !selected.Ready {
		// A runtime change rechecks existing files without a model request.
		if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, selected.ID), rt.logger.Printf); err != nil {
			return "", settings, err
		}
		probe := modelJob{Model: selected.ID, Revision: selected.Revision, Device: device}
		if err := rt.modelCommand(ctx, &probe, "probe", false); err != nil {
			if transientResourceError(err) {
				// The stand-in would need the same memory.
				return "", settings, err
			}
			return standIn(err, standInReasonRuntimeCheckFailed)
		}
		rt.invalidateModelInventory()
	}
	return device, settings, nil
}

// Why the selected model was not ready when a retired one stood in for it,
// as the transcript records it (portable.TranscriptionStandIn.Reason).
const (
	standInReasonInstalling         = "installing"
	standInReasonInstallFailed      = "install_failed"
	standInReasonDownloadsDisabled  = "downloads_disabled"
	standInReasonNotInstalled       = "not_installed"
	standInReasonRuntimeCheckFailed = "runtime_check_failed"
)

// notInstalledReason says why a model that is not installed is missing, from
// its install job and the download policy.
func (rt *Runtime) notInstalledReason(ctx context.Context, m modelInfo, device string) string {
	job, ok := rt.modelJobFor(ctx, m.ID, m.Revision, device)
	switch {
	case ok && modelJobRunning(job):
		return standInReasonInstalling
	case ok && (job.State == "failed" || job.State == "cancelled"):
		return standInReasonInstallFailed
	case rt.cfg.DisallowModelDownload:
		return standInReasonDownloadsDisabled
	}
	return standInReasonNotInstalled
}

// admitStandIn returns the retired model that stands in for selected on
// device while selected is not ready: one selected Replaces whose files are
// still installed (`models list` marks it replaced_by selected), preferring
// the job's own retired pin. The CUDA path runs only an fp32 one. A stand-in
// that has not passed its runtime check on this runtime is checked here,
// first, as the selected model would be; one that fails is not tried again
// until a restart. Nothing here ever downloads a retired model: there is
// only what an upgraded install still has.
func (rt *Runtime) admitStandIn(ctx context.Context, inventory []modelInfo, selected modelInfo, device string, limits resourceLimits, retiredPin string) (modelInfo, bool) {
	for _, m := range rt.standInCandidates(inventory, selected, device, retiredPin) {
		if m.Ready {
			return m, true
		}
		if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, m.ID), rt.logger.Printf); err != nil {
			return modelInfo{}, false
		}
		probe := modelJob{Model: m.ID, Revision: m.Revision, Device: device}
		if err := rt.modelCommand(ctx, &probe, "probe", false); err != nil {
			rt.logger.Printf("resource governor: retired %s (%s) cannot stand in for %s on %s: %v", m.ID, m.Revision, selected.ID, device, err)
			if transientResourceError(err) || ctx.Err() != nil {
				// Memory may free up; check it again on the next attempt.
				return modelInfo{}, false
			}
			rt.standInMu.Lock()
			if rt.standInCheckFailed == nil {
				rt.standInCheckFailed = map[string]bool{}
			}
			rt.standInCheckFailed[standInKey(m, device)] = true
			rt.standInMu.Unlock()
			continue
		}
		rt.invalidateModelInventory()
		m.Ready = true
		return m, true
	}
	return modelInfo{}, false
}

// standInCandidates lists the installed retired models that may stand in for
// selected on device, retiredPin first, leaving out any whose runtime check
// failed since the start.
func (rt *Runtime) standInCandidates(inventory []modelInfo, selected modelInfo, device, retiredPin string) []modelInfo {
	rt.standInMu.Lock()
	defer rt.standInMu.Unlock()
	var candidates []modelInfo
	for _, m := range inventory {
		if m.ReplacedBy != selected.ID || !slices.Contains(selected.Replaces, m.ID) || !m.Installed || m.Device != device ||
			(isCUDA(device) && !modelSupportsCUDA(m.ID)) || rt.standInCheckFailed[standInKey(m, device)] {
			continue
		}
		if m.ID == retiredPin {
			candidates = append([]modelInfo{m}, candidates...)
		} else {
			candidates = append(candidates, m)
		}
	}
	return candidates
}

func standInKey(m modelInfo, device string) string {
	return m.ID + "/" + m.Revision + "/" + device
}

// awaitModelInstall queues the install of a speech model a build waits for
// (the model replacing a retired pin, when retired names it, or the missing
// model of a delete-after-processing job), when downloads are allowed and no
// earlier attempt has finished, and returns the transient condition the build
// waits on. A failed or cancelled install is not retried from here, so a broken
// download does not restart on every deferral: Settings shows it with a retry,
// and a restart retries the active model's install. A "ready" job whose files
// are gone runs again. The build may still not wait: admitTranscription lets
// an installed retired model stand in while the model is missing.
func (rt *Runtime) awaitModelInstall(ctx context.Context, m modelInfo, device, retired string) error {
	wait := &resourceUnavailableError{resource: "speech model " + m.ID, awaitsModel: true}
	why := "is not installed"
	if retired != "" {
		why = "replaces retired " + retired
	}
	job, err := rt.ensureModelInstall(ctx, m, device, false)
	if err == nil && job.State == "ready" {
		// A finished install whose files are gone: run it again.
		job, err = rt.ensureModelInstall(ctx, m, device, true)
	}
	switch {
	case errors.Is(err, errModelDownloadsDisabled):
		if retired != "" {
			why += " and is not installed"
		}
		wait.detail = fmt.Sprintf("%s; downloads are disabled, so import it with cassini models import", why)
	case err != nil:
		wait.detail = fmt.Sprintf("%s; its install could not be queued: %v", why, err)
	case modelJobRunning(job):
		wait.detail = fmt.Sprintf("%s; being installed (%s)", why, modelJobProgressText(job))
	default:
		wait.detail = fmt.Sprintf("%s; its install ended %s", why, job.State)
		if job.Error != "" {
			wait.detail += " (" + job.Error + ")"
		}
		wait.detail += "; retry it in Settings"
	}
	return wait
}

func (rt *Runtime) executeBuildCLI(ctx context.Context, task buildTask) (string, error) {
	meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
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

	// Resource governor: never let a build starve or OOM the host (the ExApp can
	// run uncapped next to Nextcloud/Talk). Resolve the device before waiting
	// for RAM — an unsatisfiable explicit override can never become eligible
	// merely because host memory frees up — and size the RAM floor for the
	// device/model pair that resolution produced. Then wait, bounded, for
	// headroom.
	limits := resourceLimitsFromEnv()
	settings := rt.currentSettings()
	job, policyErr := rt.store.GetJob(ctx, task.JobID)
	if policyErr != nil {
		return meetingPath, policyErr
	}
	dispose := deletesSourceMedia(job)
	if dispose {
		policy := processingPolicy(job)
		if policy.Transcription == nil {
			return meetingPath, fmt.Errorf("missing saved transcription policy")
		}
		settings = *policy.Transcription
	}
	device, model, mode, reason := deviceCPU, "", "off", "disabled"
	atCeiling := task.DeferralCount >= rt.buildResourceDeferralLimit()
	// keepAudio drops transcription from this build. At the retry ceiling it is
	// better to publish the audio than to block the job.
	//
	// Running out of memory or GPU at the ceiling is a failure the run has to
	// show (D-854); a model that cannot run at all is not something waiting
	// would have fixed. The cause is kept on the attempt either way.
	keepAudio := func(cause error) {
		rt.logger.Printf("resource governor: job %s transcription unavailable, preserving audio: %v", task.JobID, cause)
		device, model, mode, reason = deviceCPU, "", "off", transcriptionReasonModelUnavailable
		if transientResourceError(cause) {
			reason = transcriptionReasonInsufficientResources
		}
		if err := rt.store.SetAttemptTranscriptionDetail(context.Background(), task.JobID, task.AttemptNumber, cause.Error()); err != nil {
			rt.logger.Printf("transcription detail update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, err)
		}
	}
	if settings.TranscriptionEnabled {
		selectedDevice, admitted, err := rt.admitTranscription(ctx, settings, limits, dispose)
		switch {
		case err == nil:
			// A retired pin runs as its replacement: the child gets the model
			// and revision admission checked, not the retired ones. A retired
			// model standing in for its replacement is what admission checked.
			settings = admitted
			device, model, mode, reason = selectedDevice, admitted.ActiveModel, "on", ""
		case ctx.Err() != nil:
			return meetingPath, ctx.Err()
		case transientResourceError(err) && !atCeiling:
			// Waiting can make transcription possible: a GPU that is not yet
			// visible, VRAM or RAM held by a neighbour, a model check that timed
			// out. Publishing audio-only now would be permanent, so defer like
			// any other resource wait and fall back only at the retry ceiling.
			return meetingPath, err
		default:
			if dispose {
				return meetingPath, err
			}
			keepAudio(err)
		}
	}
	rt.logger.Printf("resource governor: job %s transcription=%s device=%s model=%s reason=%s stand_in_for=%s", task.JobID, mode, device, model, reason, settings.StandInFor)
	if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, model), rt.logger.Printf); err != nil {
		if dispose || mode != "on" || !atCeiling || !transientResourceError(err) {
			return meetingPath, err
		}
		// Still short of RAM for the model at the ceiling; audio needs far less.
		keepAudio(err)
		if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, model), rt.logger.Printf); err != nil {
			return meetingPath, err
		}
	}
	// Use the admission snapshot, even if settings changed during resource waits.
	env := settings.ChildEnv(rt.childEnv())
	env = setEnvKey(env, envCacheRoot, rt.cfg.ModelCacheRoot)
	env = setEnvKey(env, envDisallowModelDownload, "1")
	env = setEnvKey(env, "CASSINI_TRANSCRIPTION", mode)
	env = setEnvKey(env, "CASSINI_TRANSCRIPTION_REASON", reason)
	buildEnv, err := limits.applyToEnv(env, device, model)
	if !dispose && err != nil && mode == "on" && atCeiling && transientResourceError(err) {
		// Still no VRAM at the retry ceiling: keep the audio rather than block.
		keepAudio(err)
		env = setEnvKey(setEnvKey(env, "CASSINI_TRANSCRIPTION", mode), "CASSINI_TRANSCRIPTION_REASON", reason)
		buildEnv, err = limits.applyToEnv(env, device, model)
	}
	if err != nil {
		return meetingPath, err
	}

	buildArgs := []string{"build", task.ArtifactRunPath, "--out", meetingPath}
	policy, err := rt.rebuildAudioEncodePolicy(task.JobID)
	if err != nil {
		return meetingPath, err
	}
	if policy != "" {
		buildArgs = append(buildArgs, "--audio-encode", policy)
	}
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, buildArgs...)
	// The last of the build's output, so a crash can say why without the log.
	tail := newTailBuffer(8 << 10)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile, tail)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile, tail)
	buildEnv, err = rt.mediaScratchEnv(buildEnv, task.JobID, task.AttemptNumber)
	if err != nil {
		return meetingPath, err
	}
	cmd.Env = buildEnv
	// Kill the whole process group on ctx cancel so transcriber/ffmpeg
	// grandchildren don't outlive the build.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
		if dispose {
			return meetingPath, fmt.Errorf("cassini build: %w", err)
		}
		// A fatal native inference error can terminate the recorder. A checkpoint
		// proves media preparation completed, so retry only the media path.
		_, prepared := os.Stat(filepath.Join(meetingPath, ".transcription-started"))
		if mode != "on" || ctx.Err() != nil || prepared != nil {
			return meetingPath, fmt.Errorf("cassini build: %w", err)
		}
		// The crashed attempt left a partial bundle, and a meeting build only
		// writes into an empty directory. The directory belongs to this attempt
		// alone; the capture it is built from is untouched.
		if cleanErr := os.RemoveAll(meetingPath); cleanErr != nil {
			return meetingPath, fmt.Errorf("preserve audio after transcription failure: %w", cleanErr)
		}
		// Keep why it crashed (CUDA out of memory, a cuDNN failure…) before
		// the media-only rebuild writes over the tail.
		if detailErr := rt.store.SetAttemptTranscriptionDetail(context.Background(), task.JobID, task.AttemptNumber, crashDetail(tail.String(), err)); detailErr != nil {
			rt.logger.Printf("transcription detail update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, detailErr)
		}
		fallback := exec.CommandContext(ctx, rt.cfg.CassiniBin, append(buildArgs, "--transcription", "off")...)
		fallback.Stdout, fallback.Stderr = cmd.Stdout, cmd.Stderr
		fallback.Env = setEnvKey(setEnvKey(buildEnv, "CASSINI_TRANSCRIPTION", "off"), "CASSINI_TRANSCRIPTION_REASON", transcriptionReasonFailed)
		fallback.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		fallback.Cancel = func() error { return killProcessGroup(fallback.Process) }
		if fallbackErr := fallback.Run(); fallbackErr != nil {
			return meetingPath, fmt.Errorf("preserve audio after transcription failure: %w", fallbackErr)
		}
	}
	if _, err := os.Stat(filepath.Join(meetingPath, "cassini.json")); err != nil {
		return meetingPath, fmt.Errorf("build output missing cassini.json: %w", err)
	}
	if dispose {
		if err := requireCompletedTranscription(meetingPath); err != nil {
			return meetingPath, err
		}
		if err := stampMediaPolicy(meetingPath, job); err != nil {
			return meetingPath, err
		}
	}
	return meetingPath, nil
}

func (rt *Runtime) extractBuildFailureDetail(meetingPath string, fallback error) string {
	if strings.TrimSpace(meetingPath) != "" {
		if manifest, ok, err := LoadMeetingBundleManifest(meetingPath); err == nil && ok {
			stage := strings.TrimSpace(manifest.Stage)
			errText := strings.TrimSpace(manifest.Error)
			switch {
			case stage != "" && errText != "":
				return fmt.Sprintf("build stage %s: %s", stage, errText)
			case errText != "":
				return errText
			case stage != "":
				return fmt.Sprintf("build stage %s failed", stage)
			}
		}
	}
	if fallback == nil {
		return "build failed"
	}
	return fallback.Error()
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
