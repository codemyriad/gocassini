package operator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const defaultBuildWorkerCount = 1

type buildTask struct {
	JobID           string
	AttemptNumber   int
	ArtifactRunPath string
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
	startedAt := nowUTCString()
	claimed, err := rt.store.ClaimBuildRunning(context.Background(), task.JobID, startedAt)
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
	rt.logger.Printf("build succeeded id=%s attempt=%d worker=%d attempt_meeting=%s canonical_meeting=%s seal_queued_at=%s", task.JobID, task.AttemptNumber, workerIndex, attemptMeetingPath, canonicalMeetingPath, finishedAt)
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
	// run uncapped next to Nextcloud/Talk). Wait — bounded — for RAM headroom,
	// then size STT threads and the GPU choice to what is actually available.
	limits := resourceLimitsFromEnv()
	if err := limits.waitForMemory(ctx, rt.logger.Printf); err != nil {
		return meetingPath, err
	}

	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, "build", task.ArtifactRunPath, "--out", meetingPath)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile)
	env := rt.currentSettings().ChildEnv(os.Environ())
	cmd.Env = limits.applyToEnv(env, rt.buildIntendsCUDA(), rt.logger.Printf)
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
