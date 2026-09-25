package operator

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Legacy names remain accepted by the deprecated CLI/environment option.
// They no longer select a pruner. Successful duplicate cleanup below is
// independent of age policies; retention_worker.go owns all optional expiry.
const (
	artifactRetentionAll        = "all"
	artifactRetentionSuperseded = "superseded"
	artifactRetentionSealed     = "sealed"
)

func artifactRetentionNames() []string {
	names := []string{artifactRetentionAll, artifactRetentionSuperseded, artifactRetentionSealed}
	sort.Strings(names)
	return names
}

// Retain syntax validation for legacy deployment values during deprecation.
// None of these values changes the saved calendar policies.
func validateArtifactRetentionName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, known := range artifactRetentionNames() {
		if name == known {
			return nil
		}
	}
	return fmt.Errorf("unknown artifact retention policy %q (known policies: %s)", name, strings.Join(artifactRetentionNames(), ", "))
}

// pruneArtifactsForJob removes only proven successful duplicates, independent
// of age policies. It never turns a successful publication into a failed job.
func (rt *Runtime) pruneArtifactsForJob(jobID string) {
	if !validArtifactJob(jobID) || rt.pendingArtifactOperation(jobID) {
		return
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), jobID)
	if err != nil {
		return
	}
	var current int
	_ = rt.store.db.QueryRow(`SELECT published_attempt FROM artifact_availability WHERE job_id=?`, jobID).Scan(&current)
	for _, a := range attempts {
		rt.removeSuccessfulCaptureDuplicate(jobID, a.AttemptNumber)
		if a.State != "succeeded" || a.PublishFinishedAt == nil {
			continue
		}
		paths := []string{attemptSitePath(rt.cfg.WorkRoot, jobID, a.AttemptNumber)}
		if a.AttemptNumber == current {
			paths = append(paths, attemptMeetingPath(rt.cfg.WorkRoot, jobID, a.AttemptNumber))
		}
		for _, p := range paths {
			if err := validateArtifactTree(rt.cfg.WorkRoot, p); err == nil {
				err = os.RemoveAll(p)
				if err != nil {
					rt.logger.Printf("duplicate cleanup failed job=%s: %v", jobID, err)
				}
			}
		}
	}
}

func (rt *Runtime) removeSuccessfulCaptureDuplicate(jobID string, attempt int) {
	if !validArtifactJob(jobID) {
		return
	}
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil || job.ArtifactRunPath == nil || *job.ArtifactRunPath != canonicalRunPath(rt.cfg.WorkRoot, jobID) {
		return
	}
	if _, err := requireReadyRunBundle(*job.ArtifactRunPath); err != nil {
		return
	}
	p := attemptRunPath(rt.cfg.WorkRoot, jobID, attempt)
	// Only the initial recording can own a promoted capture duplicate.
	attempts, err := rt.store.ListJobAttempts(context.Background(), jobID)
	if err != nil {
		return
	}
	for _, a := range attempts {
		if a.AttemptNumber == attempt && a.RecordFinishedAt != nil && a.ArtifactRunPath != nil && *a.ArtifactRunPath == p {
			if err := validateArtifactTree(rt.cfg.WorkRoot, p); err != nil {
				return
			}
			if err := os.RemoveAll(p); err != nil {
				rt.logger.Printf("capture duplicate cleanup failed job=%s: %v", jobID, err)
			}
			return
		}
	}
}

// reconcileArtifactDuplicatesOnStartup finishes unconditional lifecycle cleanup
// left by a stopped operator. Timed expiry belongs exclusively to the retention
// worker and never calls this pass.
func (rt *Runtime) reconcileArtifactDuplicatesOnStartup() {
	jobs, err := rt.store.ListJobs(context.Background())
	if err != nil {
		rt.logger.Printf("startup artifact reconciliation failed: %v", err)
		return
	}
	for _, job := range jobs {
		// Only terminal jobs. Anything still in flight owns its attempt
		// directories, and a worker is reading them right now.
		if job.Stage != "done" {
			continue
		}
		attempts, _ := rt.store.ListJobAttempts(context.Background(), job.ID)
		for _, a := range attempts {
			if a.State == "succeeded" && a.PublishFinishedAt != nil {
				if err := rt.promotePublishedPair(job.ID, a.AttemptNumber); err != nil {
					rt.logger.Printf("archive adoption skipped job=%s: %v", job.ID, err)
				}
				break
			}
		}
		rt.pruneArtifactsForJob(job.ID)
	}
}
