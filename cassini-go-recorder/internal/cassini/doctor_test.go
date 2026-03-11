package cassini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriberCacheChecksIncludeRemediationForLockedWhisperDir(t *testing.T) {
	tmp := t.TempDir()
	cacheRoot := filepath.Join(tmp, "cache")
	locksDir := filepath.Join(cacheRoot, "whisper", ".locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatalf("mkdir locks dir: %v", err)
	}
	if err := os.Chmod(locksDir, 0o555); err != nil {
		t.Fatalf("chmod locks dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(locksDir, 0o755)
	})

	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)
	checks := transcriberCacheChecks()
	found := false
	for _, check := range checks {
		if strings.Contains(check.summary, "whisper lock directory") && check.status == doctorFail {
			found = true
			if !strings.Contains(check.advice, "CASSINI_CACHE_ROOT") {
				t.Fatalf("expected cache remediation advice, got %#v", check)
			}
		}
	}
	if !found {
		t.Fatalf("expected whisper lock directory failure, got %#v", checks)
	}
}
