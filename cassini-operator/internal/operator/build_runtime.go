package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
			if unavailable.permanent || nextDeferral > maxDeferrals {
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
	roomToken, meetingTitle := rt.talkRoomForJob(task.JobID)
	if err := SetMeetingBundleRoom(builtMeetingPath, meetingTitle, roomToken, meetingTitle, task.JobID, task.AttemptNumber); err != nil {
		rt.logger.Printf("meeting room stamp failed id=%s meeting=%s: %v (viewer falls back to Untitled meeting; the meeting will carry no room)", task.JobID, builtMeetingPath, err)
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
// it. A non-permanent *resourceUnavailableError means waiting may help; any
// other error means this build cannot transcribe.
func (rt *Runtime) admitTranscription(ctx context.Context, settings STTSettings, limits resourceLimits) (string, string, error) {
	device, err := resolveDeviceForSettings(settings)
	if err != nil {
		return "", "", err
	}
	if settings.ActiveModel == "" || settings.ActiveRevision == "" {
		return "", "", errors.New("no speech model is selected")
	}
	inventory, err := rt.modelInventory(ctx, device)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// A slow host, not a broken install.
			return "", "", &resourceUnavailableError{resource: "speech model inventory", detail: err.Error()}
		}
		return "", "", err
	}
	selected, err := findModel(inventory, settings.ActiveModel, settings.ActiveRevision)
	if err != nil {
		return "", "", err
	}
	if !selected.Installed {
		return "", "", fmt.Errorf("speech model %s %s is not installed", selected.ID, selected.Revision)
	}
	if !selected.Ready {
		// A runtime change rechecks existing files without a model request.
		if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, selected.ID), rt.logger.Printf); err != nil {
			return "", "", err
		}
		probe := modelJob{Model: selected.ID, Revision: selected.Revision, Device: device}
		if err := rt.modelCommand(ctx, &probe, "probe", false); err != nil {
			return "", "", err
		}
		rt.invalidateModelInventory()
	}
	return device, selected.ID, nil
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
	device, model, mode, reason := deviceCPU, "", "off", "disabled"
	atCeiling := task.DeferralCount >= rt.buildResourceDeferralLimit()
	// keepAudio drops transcription from this build. At the retry ceiling it is
	// better to publish the audio than to block the job.
	keepAudio := func(cause error) {
		rt.logger.Printf("resource governor: job %s transcription unavailable, preserving audio: %v", task.JobID, cause)
		device, model, mode, reason = deviceCPU, "", "off", "model_unavailable"
	}
	if settings.TranscriptionEnabled {
		selectedDevice, selectedModel, err := rt.admitTranscription(ctx, settings, limits)
		switch {
		case err == nil:
			device, model, mode, reason = selectedDevice, selectedModel, "on", ""
		case ctx.Err() != nil:
			return meetingPath, ctx.Err()
		case transientResourceError(err) && !atCeiling:
			// Waiting can make transcription possible: a GPU that is not yet
			// visible, VRAM or RAM held by a neighbour, a model check that timed
			// out. Publishing audio-only now would be permanent, so defer like
			// any other resource wait and fall back only at the retry ceiling.
			return meetingPath, err
		default:
			keepAudio(err)
		}
	}
	rt.logger.Printf("resource governor: job %s transcription=%s device=%s model=%s reason=%s", task.JobID, mode, device, model, reason)
	if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, model), rt.logger.Printf); err != nil {
		if mode != "on" || !atCeiling || !transientResourceError(err) {
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
	if err != nil && mode == "on" && atCeiling && transientResourceError(err) {
		// Still no VRAM at the retry ceiling: keep the audio rather than block.
		keepAudio(err)
		env = setEnvKey(setEnvKey(env, "CASSINI_TRANSCRIPTION", mode), "CASSINI_TRANSCRIPTION_REASON", reason)
		buildEnv, err = limits.applyToEnv(env, device, model)
	}
	if err != nil {
		return meetingPath, err
	}

	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, "build", task.ArtifactRunPath, "--out", meetingPath)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile)
	cmd.Env = buildEnv
	// Kill the whole process group on ctx cancel so transcriber/ffmpeg
	// grandchildren don't outlive the build.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
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
		fallback := exec.CommandContext(ctx, rt.cfg.CassiniBin, "build", task.ArtifactRunPath, "--out", meetingPath, "--transcription", "off")
		fallback.Stdout, fallback.Stderr = cmd.Stdout, cmd.Stderr
		fallback.Env = setEnvKey(setEnvKey(buildEnv, "CASSINI_TRANSCRIPTION", "off"), "CASSINI_TRANSCRIPTION_REASON", "transcription_failed")
		fallback.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		fallback.Cancel = func() error { return killProcessGroup(fallback.Process) }
		if fallbackErr := fallback.Run(); fallbackErr != nil {
			return meetingPath, fmt.Errorf("preserve audio after transcription failure: %w", fallbackErr)
		}
	}
	if _, err := os.Stat(filepath.Join(meetingPath, "cassini.json")); err != nil {
		return meetingPath, fmt.Errorf("build output missing cassini.json: %w", err)
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
