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
