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

	envSourceAudioIngestEnabled = "CASSINI_SOURCE_AUDIO_INGEST"
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
	// MaxBuildWorkers controls queue consumers, not simultaneous GPU inference.
	// Hold one process-wide admission lock across claim, resource checks, and the
	// complete build so two workers cannot both observe the same RAM/VRAM as free.
	rt.buildExecutionMu.Lock()
	defer rt.buildExecutionMu.Unlock()
	if rt.ctx.Err() != nil {
		return
	}

	// Read what this build is about to consume BEFORE it runs, and stamp it
	// only if it succeeds (D-698). An upload that lands while this build is
	// working has already bumped the counter, and stamping the value as it
	// stands at the END would swallow audio this build never opened: counted as
	// consumed, with no rebuild ever owed for it. The digest is read here for
	// the same reason — it describes the bytes this build is going to see.
	consumedSourceAudio, sourceAudioDigest := rt.sourceAudioConsumption(task.JobID)

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

	attemptMeetingPath, err := rt.buildJobFn(rt.ctx, task)
	finishedAt := nowUTCString()
	if err != nil {
		var unavailable *resourceUnavailableError
		if errors.As(err, &unavailable) {
			maxDeferrals := rt.maxBuildResourceDeferrals
			if maxDeferrals <= 0 {
				maxDeferrals = defaultMaxBuildResourceDeferrals
			}
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
		detail := rt.extractBuildFailureDetail(attemptMeetingPath, err)
		rt.logger.Printf("build failed id=%s attempt=%d worker=%d: %s", task.JobID, task.AttemptNumber, workerIndex, detail)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), task.JobID, attemptMeetingPath, detail, finishedAt); updateErr != nil {
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
	if err := SetMeetingBundleRoom(attemptMeetingPath, meetingTitle, roomToken, meetingTitle, task.JobID, task.AttemptNumber); err != nil {
		rt.logger.Printf("meeting room stamp failed id=%s meeting=%s: %v (viewer falls back to Untitled meeting; the meeting will carry no room)", task.JobID, attemptMeetingPath, err)
	}
	canonicalMeetingPath, promoteErr := promoteMeetingBundle(rt.cfg.WorkRoot, attemptMeetingPath, task.JobID)
	if promoteErr != nil {
		rt.logger.Printf("build promote failed id=%s attempt=%d worker=%d meeting=%s: %v", task.JobID, task.AttemptNumber, workerIndex, attemptMeetingPath, promoteErr)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), task.JobID, attemptMeetingPath, promoteErr.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("build promote failure update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	// Hand off to the seal worker, not to publish. Sealing the portable `.opus`
	// used to be a detached goroutine started right here, after publish was
	// already queued: best-effort, unordered across reruns, and invisible when
	// it failed. It is a stage now, and its success is what makes the job
	// publishable (D-583). The hand-off is still non-blocking, so the reason it
	// was detached in the first place — a single build worker starved by an
	// ffmpeg pack — still does not apply.
	if err := rt.enqueueSealJobNonBlocking(task.JobID, task.AttemptNumber, canonicalMeetingPath, attemptMeetingPath, finishedAt); err != nil {
		rt.logger.Printf("seal queue update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, err)
		if updateErr := rt.store.MarkSealFailed(context.Background(), task.JobID, "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("seal queue failure update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	// Only now, and only with the figures read at claim time. A build that
	// failed, was interrupted or was deferred stamps nothing, so its uploads
	// stay owed and the next dispatcher pass judges the job again.
	if hasSourceAudioToRecord(consumedSourceAudio, sourceAudioDigest) {
		// Retried like every other write in this mechanism. A busy database at
		// the one moment a build finishes would otherwise leave the debt
		// standing and cost a whole redundant re-transcription.
		if err := retrySourceAudioWrite(context.Background(), func() error {
			return rt.store.MarkSourceAudioBuilt(context.Background(), task.JobID, consumedSourceAudio, sourceAudioDigest)
		}); err != nil {
			rt.logger.Printf("source audio: could not record what id=%s consumed: %v", task.JobID, err)
		}
	}
	rt.logger.Printf("build succeeded id=%s attempt=%d worker=%d attempt_meeting=%s canonical_meeting=%s seal_queued_at=%s", task.JobID, task.AttemptNumber, workerIndex, attemptMeetingPath, canonicalMeetingPath, finishedAt)
}

// sourceAudioConsumption reads the upload counter and the capture digest a
// build is about to consume. Both are best-effort: a build must not fail
// because the bookkeeping for a late upload could not be read. A zero counter
// means "nothing to stamp", which is also the answer for every installation
// that never collected a capture.
func (rt *Runtime) sourceAudioConsumption(jobID string) (int64, string) {
	if rt.store == nil {
		return 0, ""
	}
	// Nothing is consumed with ingestion off. The build below is not given
	// --source-audio at all, so stamping the counter would record that a build
	// had read audio it was never shown — and turning ingestion on afterwards
	// would find no debt and never use it. Leaving the debt owed is what makes
	// that switch recoverable.
	if !sourceAudioIngestEnabled() {
		return 0, ""
	}
	seq, err := rt.store.SourceAudioUploadSeq(context.Background(), jobID)
	if err != nil {
		rt.logger.Printf("source audio: could not read the upload counter for id=%s: %v", jobID, err)
		return 0, ""
	}
	// The scan runs even at seq == 0. An upload that landed before the build and
	// was never attributed -- one for a recording that was still live, say --
	// leaves the counter at zero while the build reads and splices it anyway, so
	// skipping the scan here would leave the digest empty and let the very same
	// capture, re-uploaded afterwards, buy a full re-transcription that produces
	// the identical transcript.
	set, err := rt.sourceCaptureSetForJob(context.Background(), jobID)
	if err != nil {
		// The counter alone is still worth stamping, and it settles the debt:
		// a build that could not read the capture root will not read it any
		// better on a second pass, and leaving the debt owed would re-run this
		// meeting on every scan. The empty digest records that this build
		// cannot say what it consumed, so a genuinely new upload afterwards is
		// still owed a rebuild and simply cannot be proved redundant.
		rt.logger.Printf("source audio: could not read the captures for id=%s: %v", jobID, err)
		return seq, ""
	}
	return seq, set.Digest
}

// hasSourceAudioToRecord reports whether a finished build has anything worth
// stamping: uploads it owed, or a capture set it can name. Either alone is
// enough, and neither means the write is skipped entirely -- which is every
// installation that never collected a capture.
func hasSourceAudioToRecord(consumed int64, digest string) bool {
	return consumed > 0 || digest != ""
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

// sourceAudioIngestEnabled reports whether transcripts may be built from
// participant-uploaded audio.
//
// On by default on this branch, which exists to run the feature: set
// CASSINI_SOURCE_AUDIO_INGEST=0 to opt out. An unreadable value is treated as
// 0. See the call site for what it costs, and note that an installation which
// never collected a capture is unaffected either way — with no upload to place,
// ingestion has nothing to substitute and the transcript is the recorded track.
func sourceAudioIngestEnabled() bool {
	return parseBoolEnvDefault(envSourceAudioIngestEnabled, true)
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
	device, admissionErr := resolveDeviceForSettings(settings)
	if admissionErr != nil {
		return meetingPath, admissionErr
	}
	model, modelErr := rt.admitModelForDevice(settings, device)
	if modelErr != nil {
		return meetingPath, modelErr
	}
	// Say which device won before any audio is decoded: a CPU build is a
	// legitimate outcome but a much slower one, and an administrator reading
	// the attempt log should never have to infer it from the elapsed time.
	rt.logger.Printf("resource governor: job %s admitted on %s (quality=%s model=%s)",
		task.JobID, device, normalizeQuality(settings.Quality), model)
	if rt.modelNeedsDownload(model) {
		// A one-off fetch of several hundred MB delays the first build of this
		// tier. Say so, or the build looks stalled.
		rt.logger.Printf("resource governor: job %s downloads model %s into %s before it transcribes; this happens once",
			task.JobID, model, rt.cfg.ModelCacheRoot)
	}
	if err := limits.waitForMemory(ctx, limits.minFreeMemForBuild(device, model), rt.logger.Printf); err != nil {
		return meetingPath, err
	}
	// Probe free VRAM only after any RAM wait, immediately before launch. That
	// reading is an admission snapshot; taking it before a long memory wait
	// would let another workload consume the GPU in between.
	env := settings.ChildEnv(os.Environ())
	if root := strings.TrimSpace(rt.cfg.ModelCacheRoot); root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return meetingPath, fmt.Errorf("create model cache root %s: %w", root, err)
		}
		env = setEnvKey(env, envCacheRoot, root)
	}
	if rt.cfg.DisallowModelDownload {
		// ChildEnv strips this so an inherited value cannot decide policy. The
		// operator puts back exactly what the administrator configured.
		env = setEnvKey(env, envDisallowModelDownload, "1")
	}
	buildEnv, err := limits.applyToEnv(env, device, model)
	if err != nil {
		return meetingPath, err
	}

	buildArgs := []string{"build", task.ArtifactRunPath, "--out", meetingPath}
	// Source-audio ingestion is ON unless an administrator turns it off with
	// CASSINI_SOURCE_AUDIO_INGEST=0. This branch exists to run the feature end
	// to end, so it runs by default here.
	//
	// What that costs is still worth stating: substituting a participant's own
	// recording into the transcript is a judgement about where somebody's words
	// belong, and the offset half of that judgement still carries client clock
	// skew (docs/source-audio-capture.md). Until the correlation refinement
	// lands, an installation whose clients are not time-synchronised should opt
	// out. An installation that never collected a capture is unaffected: the
	// build below is handed a root with nothing in it for this room, finds no
	// upload to place, and transcribes the recorded track exactly as before.
	if sourceAudioIngestEnabled() {
		root := strings.TrimSpace(rt.cfg.CaptureRoot)
		binding, hasBinding := rt.talkBindingForJob(task.JobID)
		roomToken := ""
		if hasBinding {
			roomToken = strings.TrimSpace(binding.RoomToken)
		}
		// Both, or neither. Passing --source-audio without a room token leaves
		// the build matching on call window alone, across every room this
		// installation has ever captured — which is how one meeting's audio
		// ends up in another's transcript. A job whose room cannot be resolved
		// simply does not get source audio.
		if root != "" && roomToken != "" {
			buildArgs = append(buildArgs, "--source-audio", root, "--source-audio-room", roomToken)
		} else if root != "" {
			rt.logger.Printf("build %s: source-audio ingestion enabled but no room token is known for this job; skipping", task.JobID)
		}
	}
	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, buildArgs...)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile)
	cmd.Env = buildEnv
	// Kill the whole process group on ctx cancel so transcriber/ffmpeg
	// grandchildren don't outlive the build.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
		return meetingPath, fmt.Errorf("cassini build: %w", err)
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
