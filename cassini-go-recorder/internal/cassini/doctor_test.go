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

func TestModelFilesCheckAcceptsEachQualityTiersLayout(t *testing.T) {
	// The three quality tiers ship two different architectures: the 110M "fast"
	// model is CTC (one model.int8.onnx), the 0.6B tiers are transducers, and
	// the fp32 one adds an external-weights sidecar. A check that names one
	// layout fails a bundled model that is present and correct (D-702).
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")
	for _, model := range []transcribe.ModelID{
		transcribe.ModelParakeet110M,
		transcribe.ModelParakeet06BV3Int8,
		transcribe.ModelParakeet06BV3,
	} {
		t.Run(string(model), func(t *testing.T) {
			required := transcribe.RequiredModelFileNames(model)
			if len(required) == 0 {
				t.Fatalf("no required files known for %s", model)
			}
			modelDir := t.TempDir()
			for _, name := range required {
				if err := os.WriteFile(filepath.Join(modelDir, name), []byte("x"), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			if check := modelFilesCheck(modelDir, model); check.status != doctorOK {
				t.Fatalf("modelFilesCheck(%s) = %v (%s), want ok", model, check.status, check.summary)
			}

			// Removing any one of them must fail the check, not pass silently.
			victim := filepath.Join(modelDir, required[0])
			if err := os.Remove(victim); err != nil {
				t.Fatalf("remove %s: %v", victim, err)
			}
			check := modelFilesCheck(modelDir, model)
			if check.status != doctorFail {
				t.Fatalf("modelFilesCheck(%s) with %s removed = %v, want fail", model, required[0], check.status)
			}
			if !strings.Contains(check.summary, required[0]) {
				t.Errorf("failure summary %q does not name the missing file %s", check.summary, required[0])
			}
		})
	}
}

func TestModelFilesCheckWarnsForAnUnknownModel(t *testing.T) {
	check := modelFilesCheck(t.TempDir(), transcribe.ModelID("some-future-model"))
	if check.status != doctorWarn {
		t.Fatalf("modelFilesCheck(unknown) = %v, want warn", check.status)
	}
}
