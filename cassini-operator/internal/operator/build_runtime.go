package operator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultBuildWorkerCount = 1

type buildTask struct {
	JobID           string
	AttemptNumber   int
	ArtifactRunPath string
	// TriggerKind is the attempt's trigger kind (e.g. TriggerKindRerun,
	// TriggerKindBackfillGPU). The build runner reads it to decide whether to
	// inject per-attempt env additions, e.g. CASSINI_STT_ADDITIONAL_MODELS for
	// the backfill case so the same audio is transcribed by both the GPU
	// default model and the legacy CPU model.
	TriggerKind string
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
	if err := rt.store.MarkBuildRunning(context.Background(), task.JobID, startedAt); err != nil {
		rt.logger.Printf("build start update failed id=%s worker=%d: %v", task.JobID, workerIndex, err)
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
	if err := rt.enqueuePublishJob(task.JobID, task.AttemptNumber, canonicalMeetingPath, attemptMeetingPath, finishedAt); err != nil {
		rt.logger.Printf("publish queue update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, err)
		if updateErr := rt.store.MarkPublishFailed(context.Background(), task.JobID, "", "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("publish queue failure update failed id=%s attempt=%d worker=%d: %v", task.JobID, task.AttemptNumber, workerIndex, updateErr)
		}
		return
	}
	rt.logger.Printf("build succeeded id=%s attempt=%d worker=%d attempt_meeting=%s canonical_meeting=%s publish_queued_at=%s", task.JobID, task.AttemptNumber, workerIndex, attemptMeetingPath, canonicalMeetingPath, finishedAt)
}

func (rt *Runtime) enqueueBuildJob(jobID string, attemptNumber int, jobArtifactRunPath, attemptArtifactRunPath, queuedAt string) error {
	if err := rt.store.MarkBuildQueued(context.Background(), jobID, jobArtifactRunPath, attemptArtifactRunPath, queuedAt); err != nil {
		return err
	}
	task := buildTask{JobID: jobID, AttemptNumber: attemptNumber, ArtifactRunPath: jobArtifactRunPath, TriggerKind: TriggerKindInitial}
	select {
	case rt.buildQueue <- task:
		return nil
	case <-rt.ctx.Done():
		if err := rt.store.MarkBuildFailed(context.Background(), jobID, "", "build queue stopped", nowUTCString()); err != nil {
			return err
		}
		return fmt.Errorf("build queue stopped")
	}
}

// buildEnvForAttempt computes the env that the build subprocess inherits.
// Starts from the operator's process env and layers per-trigger-kind
// additions on top. Defined as a method so it can be exercised by tests
// without spawning an actual subprocess.
func (rt *Runtime) buildEnvForAttempt(task buildTask) []string {
	env := os.Environ()
	additions := envAdditionsForTriggerKind(task.TriggerKind)
	for k, v := range additions {
		env = append(env, k+"="+v)
	}
	return env
}

// envAdditionsForTriggerKind returns env key/value pairs that the build
// subprocess should have *on top of* the operator's process env for the given
// trigger kind. The map is empty for kinds with no overrides.
func envAdditionsForTriggerKind(kind string) map[string]string {
	switch kind {
	case TriggerKindBackfillGPU:
		// The backfill rerun runs the same audio through both the current
		// default model (set on the operator's process env, normally
		// parakeet-tdt-0.6b-v3 GPU) and the legacy CPU parakeet, so the new
		// v2 .opus carries both transcripts side by side. The recorder's
		// transcribe pass reads this env var directly.
		return map[string]string{
			"CASSINI_STT_ADDITIONAL_MODELS": legacyBackfillAdditionalModel,
		}
	default:
		return nil
	}
}

// legacyBackfillAdditionalModel is the model id we ask the backfill rerun to
// run *in addition to* the current STT default. Kept as a constant rather than
// an env var so the contract is explicit: the user-facing intent is "give us
// the old CPU transcript next to the new GPU one", not "let operators
// reconfigure the backfill set per deploy".
const legacyBackfillAdditionalModel = "parakeet-tdt-0.6b"

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

	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, "build", task.ArtifactRunPath, "--out", meetingPath)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile)
	cmd.Env = rt.buildEnvForAttempt(task)
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
