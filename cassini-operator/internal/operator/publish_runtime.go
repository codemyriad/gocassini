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

type publishTask struct {
	JobID         string
	AttemptNumber int
}

func (rt *Runtime) startPublishWorker() {
	go rt.publishWorker()
}

func (rt *Runtime) publishWorker() {
	for {
		select {
		case <-rt.ctx.Done():
			return
		case task := <-rt.publishQueue:
			rt.runPublishJob(task)
		}
	}
}

func (rt *Runtime) runPublishJob(task publishTask) {
	startedAt := nowUTCString()
	if err := rt.store.MarkPublishRunning(context.Background(), task.JobID, startedAt); err != nil {
		rt.logger.Printf("publish start update failed id=%s: %v", task.JobID, err)
		return
	}
	attemptSiteDir := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
	rt.logger.Printf("publish started id=%s attempt=%d input=%s attempt_site=%s site=%s", task.JobID, task.AttemptNumber, currentRoot(rt.cfg.WorkRoot), attemptSiteDir, rt.cfg.SiteRoot)

	attemptArtifactSitePath, err := rt.publishJobFn(rt.ctx, task)
	finishedAt := nowUTCString()
	if err != nil {
		detail := rt.extractSiteFailureDetail(attemptArtifactSitePath, err)
		rt.logger.Printf("publish failed id=%s attempt=%d: %s", task.JobID, task.AttemptNumber, detail)
		if updateErr := rt.store.MarkPublishFailed(context.Background(), task.JobID, "", attemptArtifactSitePath, detail, finishedAt); updateErr != nil {
			rt.logger.Printf("publish fail update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, updateErr)
		}
		return
	}
	if err := promoteSiteBundle(attemptArtifactSitePath, rt.cfg.SiteRoot, SiteBundleLineage{
		JobID:          task.JobID,
		AttemptNumber:  task.AttemptNumber,
		PublishedAtUTC: finishedAt,
	}); err != nil {
		rt.logger.Printf("publish promote failed id=%s attempt=%d attempt_site=%s site=%s: %v", task.JobID, task.AttemptNumber, attemptArtifactSitePath, rt.cfg.SiteRoot, err)
		if updateErr := rt.store.MarkPublishFailed(context.Background(), task.JobID, "", attemptArtifactSitePath, err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("publish promote failure update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, updateErr)
		}
		return
	}
	if err := rt.store.MarkPublishSucceeded(context.Background(), task.JobID, rt.cfg.SiteRoot, attemptArtifactSitePath, finishedAt); err != nil {
		rt.logger.Printf("publish success update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, err)
		return
	}
	rt.logger.Printf("publish succeeded id=%s attempt=%d attempt_site=%s site=%s", task.JobID, task.AttemptNumber, attemptArtifactSitePath, rt.cfg.SiteRoot)
}

func (rt *Runtime) enqueuePublishJob(jobID string, attemptNumber int, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt string) error {
	if err := rt.store.MarkPublishQueued(context.Background(), jobID, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt); err != nil {
		return err
	}
	task := publishTask{JobID: jobID, AttemptNumber: attemptNumber}
	select {
	case rt.publishQueue <- task:
		return nil
	case <-rt.ctx.Done():
		if err := rt.store.MarkPublishFailed(context.Background(), jobID, "", "", "publish queue stopped", nowUTCString()); err != nil {
			return err
		}
		return fmt.Errorf("publish queue stopped")
	}
}

func (rt *Runtime) executePublishCLI(ctx context.Context, task publishTask) (string, error) {
	attemptSiteDir := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
	if err := os.MkdirAll(filepath.Dir(attemptSiteDir), 0o755); err != nil {
		return attemptSiteDir, fmt.Errorf("create site parent dir: %w", err)
	}
	if err := os.RemoveAll(attemptSiteDir); err != nil {
		return attemptSiteDir, fmt.Errorf("remove previous attempt site dir: %w", err)
	}
	logPath, logFile, err := openAttemptLogFile(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber, "publish")
	if err != nil {
		return attemptSiteDir, err
	}
	defer logFile.Close()
	if err := rt.store.SetAttemptStageLogPath(context.Background(), task.JobID, task.AttemptNumber, "publish", logPath); err != nil {
		return attemptSiteDir, err
	}

	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, "publish", currentRoot(rt.cfg.WorkRoot), "--out", attemptSiteDir)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile)
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return attemptSiteDir, fmt.Errorf("cassini publish: %w", err)
	}
	if _, err := os.Stat(filepath.Join(attemptSiteDir, "cassini.json")); err != nil {
		return attemptSiteDir, fmt.Errorf("publish output missing cassini.json: %w", err)
	}
	return attemptSiteDir, nil
}

func (rt *Runtime) extractSiteFailureDetail(sitePath string, fallback error) string {
	if strings.TrimSpace(sitePath) != "" {
		if manifest, ok, err := LoadSiteBundleManifest(sitePath); err == nil && ok {
			stage := strings.TrimSpace(manifest.Stage)
			errText := strings.TrimSpace(manifest.Error)
			switch {
			case stage != "" && errText != "":
				return fmt.Sprintf("publish stage %s: %s", stage, errText)
			case errText != "":
				return errText
			case stage != "":
				return fmt.Sprintf("publish stage %s failed", stage)
			}
		}
	}
	if fallback == nil {
		return "publish failed"
	}
	return fallback.Error()
}
