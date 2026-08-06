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

type publishTask struct {
	JobID         string
	AttemptNumber int
}

func (rt *Runtime) startPublishWorker() {
	// Reconcile leftover promotion staging before any worker can promote
	// again: a crash between replaceStagedDirectory's two renames leaves the
	// destination missing with only the ".backup" copy surviving in the
	// staging root.
	rt.reconcilePromotionLeftovers()
	// Exactly one publish worker. The local sink read-modify-writes the live
	// catalog and names its temps after their destinations, so serialised
	// delivery is a correctness requirement, not a throughput choice
	// (publish_sink.go).
	go rt.publishWorker()
}

func (rt *Runtime) reconcilePromotionLeftovers() {
	var stagingRoots []string
	if strings.TrimSpace(rt.cfg.WorkRoot) != "" {
		stagingRoots = append(stagingRoots, currentStagingRoot(rt.cfg.WorkRoot))
	}
	// Nothing writes the site staging root any more — the publish sink upserts
	// into the live site instead of promoting a whole bundle into it (D-533).
	// This stays as an upgrade guard: an operator that crashed mid-promote on a
	// previous version comes back up with its live site as a ".backup" here and
	// nothing else to restore it. Removable once every deployment has started
	// at least once on a version that includes the sink.
	if strings.TrimSpace(rt.cfg.SiteRoot) != "" {
		stagingRoots = append(stagingRoots, siteStagingRoot(rt.cfg.SiteRoot))
	}
	for _, stagingRoot := range stagingRoots {
		actions, err := reconcilePromotionStaging(stagingRoot)
		for _, action := range actions {
			rt.logger.Printf("startup promotion reconcile: %s", action)
		}
		if err != nil {
			rt.logger.Printf("startup promotion reconcile failed root=%s: %v", stagingRoot, err)
		}
	}
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
	rt.logger.Printf("publish started id=%s attempt=%d attempt_site=%s sink=%s", task.JobID, task.AttemptNumber, attemptSiteDir, rt.sink().Name())

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
	// The destination is the sink's business, not the publish worker's. A sink
	// that cannot deliver returns an error and the publish fails — there is no
	// best-effort delivery (D-533).
	location, err := rt.sink().Deliver(rt.ctx, publishDelivery{
		AttemptSitePath: attemptArtifactSitePath,
		JobID:           task.JobID,
		AttemptNumber:   task.AttemptNumber,
		PublishedAtUTC:  finishedAt,
	})
	if err != nil {
		rt.logger.Printf("publish deliver failed id=%s attempt=%d sink=%s attempt_site=%s: %v", task.JobID, task.AttemptNumber, rt.sink().Name(), attemptArtifactSitePath, err)
		if updateErr := rt.store.MarkPublishFailed(context.Background(), task.JobID, "", attemptArtifactSitePath, err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("publish deliver failure update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, updateErr)
		}
		return
	}
	if err := rt.store.MarkPublishSucceeded(context.Background(), task.JobID, location, attemptArtifactSitePath, finishedAt); err != nil {
		rt.logger.Printf("publish success update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, err)
		return
	}
	rt.logger.Printf("publish succeeded id=%s attempt=%d sink=%s attempt_site=%s location=%s", task.JobID, task.AttemptNumber, rt.sink().Name(), attemptArtifactSitePath, location)
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
	// One meeting per publish, not the whole library (D-459). Resolved before
	// anything is created so a job with nothing to publish fails immediately
	// instead of spawning a subprocess that exports an empty site.
	publishInput, err := resolvePublishInputPath(rt.cfg.WorkRoot, task.JobID)
	if err != nil {
		return attemptSiteDir, err
	}
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

	cmd := exec.CommandContext(ctx, rt.cfg.CassiniBin, "publish", publishInput, "--out", attemptSiteDir)
	cmd.Stdout = io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	cmd.Stderr = io.MultiWriter(writerOrDiscard(rt.stderr), logFile)
	cmd.Env = os.Environ()
	// Kill the whole process group on ctx cancel so exporter grandchildren
	// don't outlive the publish.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
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
