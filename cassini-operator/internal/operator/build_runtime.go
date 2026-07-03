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
	for i := 0; i < rt.cfg.MaxBuildWorkers; i++ {
		go rt.buildWorker(i + 1)
	}
}

func (rt *Runtime) buildWorker(index int) {
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
	canonicalMeetingPath, promoteErr := promoteMeetingBundle(rt.cfg.WorkRoot, attemptMeetingPath, task.JobID)
	if promoteErr != nil {
		rt.logger.Printf("build promote failed id=%s attempt=%d worker=%d meeting=%s: %v", task.JobID, task.AttemptNumber, workerIndex, attemptMeetingPath, promoteErr)
		if updateErr := rt.store.MarkBuildFailed(context.Background(), task.JobID, attemptMeetingPath, promoteErr.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("build promote failure update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	// Stamp the Talk room name into the promoted bundle BEFORE publish is
	// enqueued: `cassini publish` re-packs the `.meeting` when the durable
	// `.opus` does not exist yet (the async pack below almost always loses
	// that race), and the manifest title is what keeps the very first publish
	// of a fresh recording correctly named (D-462). Best-effort: a failed
	// stamp only costs the name, never the publish.
	meetingTitle := rt.talkRoomNameForJob(task.JobID)
	if meetingTitle != "" {
		if err := SetMeetingBundleTitle(canonicalMeetingPath, meetingTitle); err != nil {
			rt.logger.Printf("meeting title stamp failed id=%s meeting=%s: %v (viewer falls back to Untitled meeting)", task.JobID, canonicalMeetingPath, err)
		}
	}
	if err := rt.enqueuePublishJobNonBlocking(task.JobID, task.AttemptNumber, canonicalMeetingPath, attemptMeetingPath, finishedAt); err != nil {
		rt.logger.Printf("publish queue update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, err)
		if updateErr := rt.store.MarkPublishFailed(context.Background(), task.JobID, "", "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("publish queue failure update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	rt.logger.Printf("build succeeded id=%s attempt=%d worker=%d attempt_meeting=%s canonical_meeting=%s publish_queued_at=%s", task.JobID, task.AttemptNumber, workerIndex, attemptMeetingPath, canonicalMeetingPath, finishedAt)

	// Store the durable `.opus` next to the promoted `.meeting` (D-428), OFF
	// the build->publish critical path. Packing shells out to `cassini pack`
	// (an ffmpeg encode); doing it synchronously before the publish hand-off
	// added latency to every build and starved the single build worker, which
	// flaked the queue/handoff tests under load. Running it after the publish
	// enqueue is safe: the meeting title publish needs already rides the
	// bundle manifest (stamped above), so nothing depends on this .opus until
	// `.meeting` is retired (D-429). A pack failure (or a cassini binary that
	// predates `cassini pack`) loses nothing: `.meeting` stays the durable
	// publish input.
	rt.opusPackWG.Add(1)
	go func() {
		defer rt.opusPackWG.Done()
		opusPath, packErr := packCanonicalMeetingToOpus(rt.ctx, rt.cfg.CassiniBin, rt.cfg.WorkRoot, task.JobID, meetingTitle, nil)
		if packErr != nil {
			rt.logger.Printf("build opus pack skipped id=%s attempt=%d worker=%d meeting=%s: %v (durable .meeting retained; publish unaffected)", task.JobID, task.AttemptNumber, workerIndex, canonicalMeetingPath, packErr)
			return
		}
		rt.logger.Printf("build opus packed id=%s attempt=%d worker=%d opus=%s", task.JobID, task.AttemptNumber, workerIndex, opusPath)
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
