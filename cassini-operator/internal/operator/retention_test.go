package operator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedAttemptArtifacts lays down the full set of attempt-scoped payloads plus
// the canonical artifacts that make them prunable.
func seedAttemptArtifacts(t *testing.T, workRoot, jobID string, attempts int, withCanonical bool) {
	t.Helper()
	for attempt := 1; attempt <= attempts; attempt++ {
		for _, dir := range []string{
			attemptRunPath(workRoot, jobID, attempt),
			attemptMeetingPath(workRoot, jobID, attempt),
			attemptSitePath(workRoot, jobID, attempt),
			attemptSealDir(workRoot, jobID, attempt),
			attemptLogsDir(workRoot, jobID, attempt),
		} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("bytes"), 0o644); err != nil {
				t.Fatalf("write payload in %s: %v", dir, err)
			}
		}
	}
	if !withCanonical {
		return
	}
	for _, dir := range []string{canonicalRunPath(workRoot, jobID), canonicalMeetingPath(workRoot, jobID)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(canonicalOpusPath(workRoot, jobID), []byte("canonical"), 0o644); err != nil {
		t.Fatalf("write canonical opus: %v", err)
	}
}

func assertExists(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s must survive pruning (%s): %v", path, why, err)
	}
}

func assertGone(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should have been pruned (%s): err=%v", path, why, err)
	}
}

func TestArtifactRetentionAllPrunesNothing(t *testing.T) {
	workRoot := t.TempDir()
	seedAttemptArtifacts(t, workRoot, "job1", 2, true)

	removed, err := pruneJobArtifacts(workRoot, "job1", 2, true, artifactRetentionAll)
	if err != nil {
		t.Fatalf("pruneJobArtifacts() error = %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("policy %q removed %v, want nothing", artifactRetentionAll, removed)
	}
	assertExists(t, attemptRunPath(workRoot, "job1", 1), "policy all")
	assertExists(t, attemptSitePath(workRoot, "job1", 2), "policy all")
}

func TestArtifactRetentionSupersededPrunesReplacedAttemptsOnly(t *testing.T) {
	workRoot := t.TempDir()
	seedAttemptArtifacts(t, workRoot, "job1", 2, true)

	if _, err := pruneJobArtifacts(workRoot, "job1", 2, true, artifactRetentionSuperseded); err != nil {
		t.Fatalf("pruneJobArtifacts() error = %v", err)
	}

	// Attempt 1 was replaced by the rerun; nothing reads it, because a rerun
	// rebuilds from current/<job>.run.
	assertGone(t, attemptRunPath(workRoot, "job1", 1), "superseded")
	assertGone(t, attemptMeetingPath(workRoot, "job1", 1), "superseded")
	assertGone(t, attemptSitePath(workRoot, "job1", 1), "superseded")
	assertGone(t, attemptSealDir(workRoot, "job1", 1), "superseded")
	// Logs are the forensic record and are never pruned.
	assertExists(t, attemptLogsDir(workRoot, "job1", 1), "logs are never pruned")
	// The current attempt is untouched under this policy, succeeded or not.
	assertExists(t, attemptRunPath(workRoot, "job1", 2), "current attempt, policy superseded")
	assertExists(t, attemptMeetingPath(workRoot, "job1", 2), "current attempt, policy superseded")
	assertExists(t, attemptSitePath(workRoot, "job1", 2), "current attempt, policy superseded")
}

func TestArtifactRetentionSealedPrunesTheSucceededAttemptButKeepsItsSeal(t *testing.T) {
	workRoot := t.TempDir()
	seedAttemptArtifacts(t, workRoot, "job1", 1, true)

	removed, err := pruneJobArtifacts(workRoot, "job1", 1, true, artifactRetentionSealed)
	if err != nil {
		t.Fatalf("pruneJobArtifacts() error = %v", err)
	}
	if len(removed) == 0 {
		t.Fatal("policy sealed removed nothing from a succeeded job")
	}

	assertGone(t, attemptRunPath(workRoot, "job1", 1), "promoted into current/")
	assertGone(t, attemptMeetingPath(workRoot, "job1", 1), "promoted into current/")
	assertGone(t, attemptSitePath(workRoot, "job1", 1), "delivered to the sink")
	// The sealed artifact stays: it is what was published, and its digest is the
	// evidence that what was published is what was sealed.
	assertExists(t, attemptSealDir(workRoot, "job1", 1), "the sealed artifact")
	assertExists(t, attemptLogsDir(workRoot, "job1", 1), "logs are never pruned")
	// current/ is never touched — it is what reruns and debugging read.
	assertExists(t, canonicalRunPath(workRoot, "job1"), "current/ is never pruned")
	assertExists(t, canonicalMeetingPath(workRoot, "job1"), "current/ is never pruned")
	assertExists(t, canonicalOpusPath(workRoot, "job1"), "current/ is never pruned")
}

func TestArtifactRetentionSealedKeepsAFailedAttempt(t *testing.T) {
	workRoot := t.TempDir()
	seedAttemptArtifacts(t, workRoot, "job1", 1, true)

	removed, err := pruneJobArtifacts(workRoot, "job1", 1, false, artifactRetentionSealed)
	if err != nil {
		t.Fatalf("pruneJobArtifacts() error = %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("a failed job had %v pruned, want nothing — it is the only evidence left", removed)
	}
	assertExists(t, attemptRunPath(workRoot, "job1", 1), "failed attempt")
	assertExists(t, attemptMeetingPath(workRoot, "job1", 1), "failed attempt")
	assertExists(t, attemptSitePath(workRoot, "job1", 1), "failed attempt")
}

// Nothing removes the last copy of anything: a record that failed before
// promotion has no canonical run to rebuild from, so its attempt run survives
// whatever the policy says.
func TestArtifactRetentionNeverRemovesTheLastCopy(t *testing.T) {
	workRoot := t.TempDir()
	seedAttemptArtifacts(t, workRoot, "job1", 2, false)

	if _, err := pruneJobArtifacts(workRoot, "job1", 2, true, artifactRetentionSealed); err != nil {
		t.Fatalf("pruneJobArtifacts() error = %v", err)
	}
	assertExists(t, attemptRunPath(workRoot, "job1", 1), "no canonical run exists")
	assertExists(t, attemptMeetingPath(workRoot, "job1", 1), "no canonical meeting exists")
	assertExists(t, attemptSealDir(workRoot, "job1", 1), "no canonical opus exists")
	assertExists(t, attemptRunPath(workRoot, "job1", 2), "no canonical run exists")
	assertExists(t, attemptMeetingPath(workRoot, "job1", 2), "no canonical meeting exists")
	// The attempt site is transient staging either way: it was delivered, and
	// the live site holds what it produced.
	assertGone(t, attemptSitePath(workRoot, "job1", 1), "superseded staging tree")
}

func TestArtifactRetentionDefaultsToSealed(t *testing.T) {
	if got := artifactRetentionOrDefault("  "); got != artifactRetentionSealed {
		t.Fatalf("unset policy = %q, want %q", got, artifactRetentionSealed)
	}
	workRoot := t.TempDir()
	seedAttemptArtifacts(t, workRoot, "job1", 1, true)
	if _, err := pruneJobArtifacts(workRoot, "job1", 1, true, ""); err != nil {
		t.Fatalf("pruneJobArtifacts() error = %v", err)
	}
	assertGone(t, attemptRunPath(workRoot, "job1", 1), "default policy is sealed")
}

func TestValidateArtifactRetentionNameRejectsUnknownPolicies(t *testing.T) {
	// Unset is not wrong; it resolves to the default.
	if err := validateArtifactRetentionName("  "); err != nil {
		t.Fatalf("empty policy must be accepted as unset, got %v", err)
	}
	for _, name := range artifactRetentionNames() {
		if err := validateArtifactRetentionName(name); err != nil {
			t.Fatalf("policy %q must be accepted: %v", name, err)
		}
	}
	err := validateArtifactRetentionName("aggressive")
	if err == nil {
		t.Fatal("expected an unknown policy to be rejected")
	}
	// The message has to say what the valid answers are — an operator who got
	// this wrong is looking at a process that will not start.
	for _, name := range artifactRetentionNames() {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %v does not name the %q policy", err, name)
		}
	}
}

func TestLoadConfigRejectsUnknownArtifactRetention(t *testing.T) {
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)

	_, code, err := loadConfig([]string{"--artifact-retention", "aggressive"}, ioDiscard{})
	if code != 2 || err == nil {
		t.Fatalf("loadConfig() code = %d err = %v, want exit 2 with an error", code, err)
	}
	if !strings.Contains(err.Error(), artifactRetentionSealed) {
		t.Fatalf("error %v must list the valid policies", err)
	}
}
