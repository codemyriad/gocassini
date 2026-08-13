package operator

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Artifact retention (D-583).
//
// Until now nothing in the operator pruned anything, and the work root grew
// with every job and every rerun. This is a deliberately narrow first policy:
// it touches attempt-scoped payloads under runs/ and nothing else.
//
//	workRoot/
//	  current/                      NEVER pruned — canonical .run/.meeting/.opus,
//	                                which is what reruns and debugging read
//	  runs/
//	    <job>--attempt-NNN.run      ┐
//	    <job>--attempt-NNN.meeting  ├─ heavy, and duplicated in current/ or transient
//	    <job>--attempt-NNN.site     │
//	    <job>--attempt-NNN.seal     ┘  (kept for the current attempt: it is the
//	                                    artifact that was delivered, and its
//	                                    digest is the evidence)
//	    <job>--attempt-NNN.logs     NEVER pruned — the forensic record, and small
//	  siteRoot/                     NEVER pruned — deleting published recordings
//	                                is a separate, user-facing decision (D-521)
//
// What stays unbounded, stated plainly: the number of jobs, current/, and the
// live site. A byte or age cap over the whole work root is a different feature
// with different failure modes, and it is not this.
const (
	// artifactRetentionAll keeps everything — the behaviour before this policy
	// existed, and the escape hatch when something needs to be dug out of a
	// completed attempt.
	artifactRetentionAll = "all"
	// artifactRetentionSuperseded prunes only attempts a rerun has replaced.
	artifactRetentionSuperseded = "superseded"
	// artifactRetentionSealed additionally prunes a succeeded attempt's inputs
	// and staging tree, keeping its sealed `.opus`.
	artifactRetentionSealed = "sealed"
	// defaultArtifactRetention is what an unset selection resolves to.
	defaultArtifactRetention = artifactRetentionSealed
)

func artifactRetentionNames() []string {
	names := []string{artifactRetentionAll, artifactRetentionSuperseded, artifactRetentionSealed}
	sort.Strings(names)
	return names
}

// validateArtifactRetentionName accepts the empty name (meaning "unset", which
// resolves to the default) and every known policy. A non-empty unrecognised name
// is an error: silently keeping everything when an operator asked for pruning,
// or silently pruning when they asked for something else, are both worse than
// refusing to start.
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

func artifactRetentionOrDefault(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultArtifactRetention
	}
	return name
}

// pruneJobArtifacts removes one job's prunable attempt payloads and returns what
// it removed, so the caller can log it — a retention policy that prunes silently
// reads, in an incident, exactly like a policy that lost data.
//
// Every removal is guarded on the artifact that replaces it actually existing.
// A record that failed before promotion keeps its attempt `.run`, because there
// is no canonical run to rebuild from; a build that failed keeps its `.meeting`
// for the same reason. Nothing here removes the last copy of anything.
func pruneJobArtifacts(workRoot, jobID string, currentAttempt int, succeeded bool, policy string) ([]string, error) {
	policy = artifactRetentionOrDefault(policy)
	if policy == artifactRetentionAll || strings.TrimSpace(workRoot) == "" || strings.TrimSpace(jobID) == "" {
		return nil, nil
	}

	var removed []string
	remove := func(path, why string) error {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("stat prunable artifact %s: %w", path, err)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("prune %s: %w", path, err)
		}
		removed = append(removed, fmt.Sprintf("%s (%s)", path, why))
		return nil
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	// Superseded attempts: a rerun replaced them, and nothing reads them. The
	// rerun rebuilds from current/<job>.run, never from an attempt directory.
	for attempt := 1; attempt < currentAttempt; attempt++ {
		if exists(canonicalRunPath(workRoot, jobID)) {
			if err := remove(attemptRunPath(workRoot, jobID, attempt), "superseded attempt"); err != nil {
				return removed, err
			}
		}
		if exists(canonicalMeetingPath(workRoot, jobID)) {
			if err := remove(attemptMeetingPath(workRoot, jobID, attempt), "superseded attempt"); err != nil {
				return removed, err
			}
		}
		if err := remove(attemptSitePath(workRoot, jobID, attempt), "superseded attempt"); err != nil {
			return removed, err
		}
		if exists(canonicalOpusPath(workRoot, jobID)) {
			if err := remove(attemptSealDir(workRoot, jobID, attempt), "superseded attempt"); err != nil {
				return removed, err
			}
		}
	}

	// The current attempt, once it has succeeded: its `.run` and `.meeting` are
	// byte-duplicated in current/, and its `.site` was a staging tree whose only
	// unique content — the `.opus` — is both sealed in the attempt's `.seal`
	// directory and delivered to the live site. The seal itself is kept: it is
	// the artifact that was published, and its digest is what proves it.
	if policy == artifactRetentionSealed && succeeded {
		if exists(canonicalRunPath(workRoot, jobID)) {
			if err := remove(attemptRunPath(workRoot, jobID, currentAttempt), "promoted into current/"); err != nil {
				return removed, err
			}
		}
		if exists(canonicalMeetingPath(workRoot, jobID)) {
			if err := remove(attemptMeetingPath(workRoot, jobID, currentAttempt), "promoted into current/"); err != nil {
				return removed, err
			}
		}
		if err := remove(attemptSitePath(workRoot, jobID, currentAttempt), "delivered to the sink"); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// pruneArtifactsForJob applies the configured policy to one job and logs every
// removal. It never fails the caller: retention is housekeeping, and a job that
// published correctly must not be reported as failed because a directory could
// not be removed.
func (rt *Runtime) pruneArtifactsForJob(jobID string) {
	job, err := rt.store.GetJob(context.Background(), jobID)
	if err != nil {
		rt.logger.Printf("artifact retention skipped id=%s: %v", jobID, err)
		return
	}
	succeeded := job.Stage == "done" && job.State == "succeeded"
	removed, err := pruneJobArtifacts(rt.cfg.WorkRoot, jobID, job.CurrentAttemptNumber, succeeded, rt.cfg.ArtifactRetention)
	for _, path := range removed {
		rt.logger.Printf("artifact retention removed id=%s policy=%s %s", jobID, artifactRetentionOrDefault(rt.cfg.ArtifactRetention), path)
	}
	if err != nil {
		rt.logger.Printf("artifact retention failed id=%s: %v", jobID, err)
	}
}

// sweepArtifactRetention applies the policy across every job at startup, so a
// deployment that has been running without one — or was restarted mid-pipeline —
// converges instead of waiting for each job to publish again.
func (rt *Runtime) sweepArtifactRetention() {
	if artifactRetentionOrDefault(rt.cfg.ArtifactRetention) == artifactRetentionAll {
		return
	}
	jobs, err := rt.store.ListJobs(context.Background())
	if err != nil {
		rt.logger.Printf("startup artifact retention sweep failed: %v", err)
		return
	}
	for _, job := range jobs {
		// Only terminal jobs. Anything still in flight owns its attempt
		// directories, and a worker is reading them right now.
		if job.Stage != "done" {
			continue
		}
		rt.pruneArtifactsForJob(job.ID)
	}
}
