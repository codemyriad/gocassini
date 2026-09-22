package cassini

import (
	"encoding/json"
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
		if strings.Contains(check.Summary, "STT model cache") && check.Status == doctorFail {
			found = true
			if !strings.Contains(check.Advice, "CASSINI_CACHE_ROOT") {
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
			if check := modelFilesCheck(modelDir, model); check.Status != doctorOK {
				t.Fatalf("modelFilesCheck(%s) = %v (%s), want ok", model, check.Status, check.Summary)
			}

			// Removing any one of them must fail the check, not pass silently.
			victim := filepath.Join(modelDir, required[0])
			if err := os.Remove(victim); err != nil {
				t.Fatalf("remove %s: %v", victim, err)
			}
			check := modelFilesCheck(modelDir, model)
			if check.Status != doctorFail {
				t.Fatalf("modelFilesCheck(%s) with %s removed = %v, want fail", model, required[0], check.Status)
			}
			if !strings.Contains(check.Summary, required[0]) {
				t.Errorf("failure summary %q does not name the missing file %s", check.Summary, required[0])
			}
		})
	}
}

func TestModelFilesCheckWarnsForAnUnknownModel(t *testing.T) {
	check := modelFilesCheck(t.TempDir(), transcribe.ModelID("some-future-model"))
	if check.Status != doctorWarn {
		t.Fatalf("modelFilesCheck(unknown) = %v, want warn", check.Status)
	}
}

func TestNativeRuntimeCheck(t *testing.T) {
	tests := []struct {
		name       string
		modelID    transcribe.ModelID
		version    string
		hasRef     bool
		wantStatus doctorStatus
		wantSubstr string
		wantAdvice string
	}{
		{
			name:       "non-v3 model ignores reference flag and passes",
			modelID:    transcribe.ModelParakeet110M,
			version:    "1.13.7",
			hasRef:     false,
			wantStatus: doctorOK,
			wantSubstr: "speech engine runtime 1.13.7",
		},
		{
			name:       "v3 model with reference frontend passes with active marker",
			modelID:    transcribe.ModelParakeet06BV3Int8,
			version:    "1.13.7+cassini-parakeet-v3-reference-v1",
			hasRef:     true,
			wantStatus: doctorOK,
			wantSubstr: SpeechEngineRefActiveMarker,
		},
		{
			name:       "v3 model without reference frontend warns (never fails) with inactive marker",
			modelID:    transcribe.ModelParakeet06BV3Int8,
			version:    "1.13.7",
			hasRef:     false,
			wantStatus: doctorWarn,
			wantSubstr: SpeechEngineRefInactiveMarker,
			wantAdvice: "LD_LIBRARY_PATH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := nativeRuntimeCheckWithState(tt.modelID, tt.version, tt.hasRef)
			if check.Status == doctorFail {
				t.Fatalf("nativeRuntimeCheck must never return doctorFail, got %+v", check)
			}
			if check.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s", check.Status, tt.wantStatus)
			}
			if !strings.Contains(check.Summary, tt.wantSubstr) {
				t.Errorf("summary %q does not contain %q", check.Summary, tt.wantSubstr)
			}
			if tt.wantAdvice != "" && !strings.Contains(check.Advice, tt.wantAdvice) {
				t.Errorf("advice %q does not contain %q", check.Advice, tt.wantAdvice)
			}
		})
	}

	// Live environment check: verify against linked runtime without stubs.
	liveCheck := nativeRuntimeCheck(transcribe.ModelParakeet06BV3Int8)
	if liveCheck.Status == doctorFail {
		t.Fatalf("live nativeRuntimeCheck returned doctorFail: %+v", liveCheck)
	}
	if liveCheck.Status != doctorOK && liveCheck.Status != doctorWarn {
		t.Fatalf("live nativeRuntimeCheck returned unexpected Status: %s", liveCheck.Status)
	}
	if !strings.Contains(liveCheck.Summary, "speech engine runtime") {
		t.Fatalf("live summary missing speech engine runtime prefix: %s", liveCheck.Summary)
	}
}

// D-798: the operator reads these checks, and it must key on something that
// survives an editing pass.
func TestEveryDoctorCheckCarriesAStableID(t *testing.T) {
	for _, target := range []string{"all", "record", "build"} {
		for _, check := range collectDoctorChecks(target) {
			if strings.TrimSpace(check.ID) == "" {
				t.Errorf("target %q: check with no id: %+v", target, check)
			}
		}
	}
}

// The check the operator's reference-frontend probe looks up by id. Named here
// so that renaming it fails a test in this module rather than silently costing
// the operator its answer — which is exactly what the prose markers did.
func TestDoctorReportsTheSpeechRuntimeCheckTheOperatorLooksUp(t *testing.T) {
	found := false
	for _, check := range collectDoctorChecks("build") {
		if check.ID == "speech.runtime" {
			found = true
			if check.Status != doctorOK && check.Status != doctorWarn {
				t.Errorf("speech.runtime status = %q; the operator maps only ok and warn", check.Status)
			}
		}
	}
	if !found {
		t.Fatal("no speech.runtime check; cassini-operator's doProbeReferenceFrontend reads it by that id")
	}
}

// --json must be the WHOLE of stdout: a caller unmarshals it directly, so a
// trailing "result ok" line would break it.
func TestDoctorJSONIsTheWholeOfStdout(t *testing.T) {
	var stdout, stderr strings.Builder
	code := runDoctor([]string{"--json", "--target", "build"}, &stdout, &stderr)

	var checks []DoctorCheck
	if err := json.Unmarshal([]byte(stdout.String()), &checks); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
	}
	if len(checks) == 0 {
		t.Fatal("no checks in the document")
	}
	if strings.Contains(stdout.String(), "result ") {
		t.Error("the verdict line leaked into the JSON output")
	}

	// The exit code is the contract /healthz?check=record depends on, and it
	// must not depend on how the output was rendered.
	var textOut, textErr strings.Builder
	if textCode := runDoctor([]string{"--target", "build"}, &textOut, &textErr); textCode != code {
		t.Errorf("exit code differs by output format: json=%d text=%d", code, textCode)
	}
}

// The text rendering is a shipped contract: a standalone `cassini doctor` must
// keep printing what it printed before.
func TestDoctorTextOutputIsUnchangedByTheJSONFlag(t *testing.T) {
	var out, errOut strings.Builder
	runDoctor([]string{"--target", "build"}, &out, &errOut)
	text := out.String()
	if !strings.Contains(text, "result ") {
		t.Error("text output lost its verdict line")
	}
	if strings.HasPrefix(strings.TrimSpace(text), "[{") {
		t.Error("text output became JSON")
	}
}
