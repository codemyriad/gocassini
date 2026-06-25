package cassini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/transcribe"
)

func TestSTTModelCacheChecksIncludeRemediationForUnwritableModelDir(t *testing.T) {
	tmp := t.TempDir()
	cacheRoot := filepath.Join(tmp, "cache")
	// Pin the device so the resolved model is deterministic regardless of
	// whether the test host has a GPU (auto-detect would pick fp32 on a GPU box).
	t.Setenv("CASSINI_STT_DEVICE", "cpu")
	modelDir := filepath.Join(cacheRoot, "models", string(transcribe.ResolveModelID("", "", "cpu")))
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir model dir: %v", err)
	}
	if err := os.Chmod(modelDir, 0o555); err != nil {
		t.Fatalf("chmod model dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(modelDir, 0o755)
	})

	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)
	checks := sttModelCacheChecks()
	found := false
	for _, check := range checks {
		if strings.Contains(check.summary, "STT model cache") && check.status == doctorFail {
			found = true
			if !strings.Contains(check.advice, "CASSINI_CACHE_ROOT") {
				t.Fatalf("expected cache remediation advice, got %#v", check)
			}
		}
	}
	if !found {
		t.Fatalf("expected STT model cache failure, got %#v", checks)
	}
}
