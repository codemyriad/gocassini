package cassini

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gocassini/internal/transcribe"
)

func TestSTTModelCacheChecksMissingIsAudioOnlyAndNeverDownloads(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CASSINI_CACHE_ROOT", root)
	t.Setenv("CASSINI_STT_DEVICE", "cpu")
	t.Setenv("CASSINI_STT_MODEL", string(transcribe.ModelParakeetUltraInt8))
	t.Setenv("CASSINI_STT_REVISION", "")
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")

	checks := sttModelCacheChecks()
	found := false
	for _, check := range checks {
		if check.Status == doctorFail {
			t.Fatalf("missing optional model must not fail doctor: %#v", check)
		}
		if check.ID == "model.ready" {
			found = true
			if check.Status != doctorWarn || !strings.Contains(check.Summary, "meetings retain audio") {
				t.Fatalf("missing model should warn about audio-only recording: %#v", check)
			}
			if !strings.Contains(check.Advice, "models import") {
				t.Fatalf("missing model should have installation advice: %#v", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing model.ready check: %#v", checks)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only doctor changed model root: %v %v", entries, err)
	}
}

func TestNativeRuntimeCheck(t *testing.T) {
	const (
		cassini   = "2.0.1+cassini.parakeet-tdt-joiner-v1.nemotron-diarization-v2"
		cassini8  = "1.13.7+cassini-parakeet-v3-reference-v1.nemotron-diarization-v2.parakeet-ultra-v1"
		stock201  = "2.0.1"
		stock1137 = "1.13.7"
	)
	tests := []struct {
		name       string
		modelID    transcribe.ModelID
		version    string
		wantStatus doctorStatus
		wantSubstr string
		wantAdvice string
	}{
		{"110M on stock 2.0.1 passes", transcribe.ModelParakeet110M, stock201, doctorOK, "speech engine runtime 2.0.1", ""},
		{"Ultra on the Cassini runtime names its frontend", transcribe.ModelParakeetUltraInt8, cassini, doctorOK, SpeechEngineFrontendMarker, ""},
		{"Ultra fp32 on the Cassini runtime", transcribe.ModelParakeetUltra, cassini, doctorOK, SpeechEngineFrontendMarker, ""},
		{"Ultra on stock 2.0.1 warns about the joiner marker", transcribe.ModelParakeetUltraInt8, stock201, doctorWarn, ".parakeet-tdt-joiner-v1", "LD_LIBRARY_PATH"},
		{"Ultra on cassini.8 warns about the old C API", transcribe.ModelParakeetUltra, cassini8, doctorWarn, "1.13.7 Cassini runtime", "LD_LIBRARY_PATH"},
		{"110M on stock 1.13.7 warns about the old C API", transcribe.ModelParakeet110M, stock1137, doctorWarn, SpeechEngineUnsupportedMarker, "LD_LIBRARY_PATH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := nativeRuntimeCheckForVersion(tt.modelID, tt.version)
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
			if strings.Contains(check.Summary, "reference frontend") {
				t.Errorf("summary %q claims the 1.13.7 reference frontend", check.Summary)
			}
		})
	}
}

// D-798: the operator reads these checks, and it must key on something that
// survives an editing pass.
func TestEveryDoctorCheckCarriesAStableID(t *testing.T) {
	t.Setenv("CASSINI_TRANSCRIPTION", "on")
	for _, target := range []string{"all", "record", "build", "media", "unsupported"} {
		seen := map[string]bool{}
		for _, check := range collectDoctorChecks(target) {
			if strings.TrimSpace(check.ID) == "" {
				t.Errorf("target %q: check with no id: %+v", target, check)
			}
			if seen[check.ID] {
				t.Errorf("target %q: duplicate id %q", target, check.ID)
			}
			seen[check.ID] = true
		}
	}
}

func TestDoctorTargetsKeepMediaChecksSeparateFromOptionalTranscription(t *testing.T) {
	t.Setenv("CASSINI_TRANSCRIPTION", "on")
	media := map[string]bool{}
	for _, check := range collectDoctorChecks("media") {
		media[check.ID] = true
	}
	if !media["ffmpeg"] || !media["ffprobe"] {
		t.Fatalf("media target omitted media tools: %#v", media)
	}
	if media["speech.runtime"] || media["model.ready"] {
		t.Fatalf("media target included optional transcription checks: %#v", media)
	}

	t.Setenv("CASSINI_TRANSCRIPTION", "off")
	for _, check := range collectDoctorChecks("build") {
		if check.ID == "speech.runtime" || strings.HasPrefix(check.ID, "model.") {
			t.Fatalf("transcription disabled, but doctor reported %q", check.ID)
		}
	}
}

// The check the operator's speech-runtime probe looks up by id. Named here
// so that renaming it fails a test in this module rather than silently costing
// the operator its answer — which is exactly what the prose markers did.
func TestDoctorReportsTheSpeechRuntimeCheckTheOperatorLooksUp(t *testing.T) {
	t.Setenv("CASSINI_TRANSCRIPTION", "on")
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
		t.Fatal("no speech.runtime check; cassini-operator's doProbeSpeechRuntime reads it by that id")
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
// keep producing human-readable checks and a verdict.
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
