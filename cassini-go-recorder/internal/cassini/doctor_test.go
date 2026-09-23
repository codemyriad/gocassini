package cassini

import (
	"os"
	"strings"
	"testing"

	"gocassini/internal/transcribe"
)

func TestSTTModelCacheChecksMissingIsAudioOnlyAndNeverDownloads(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CASSINI_CACHE_ROOT", root)
	t.Setenv("CASSINI_STT_DEVICE", "cpu")
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")
	checks := sttModelCacheChecks()
	found := false
	for _, c := range checks {
		if c.status == doctorFail {
			t.Fatalf("missing optional model must not fail doctor: %#v", c)
		}
		if strings.Contains(c.summary, "meetings retain audio") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing actionable diagnostic: %#v", checks)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only doctor changed model root: %v %v", entries, err)
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
			if check.status == doctorFail {
				t.Fatalf("nativeRuntimeCheck must never return doctorFail, got %+v", check)
			}
			if check.status != tt.wantStatus {
				t.Errorf("status = %s, want %s", check.status, tt.wantStatus)
			}
			if !strings.Contains(check.summary, tt.wantSubstr) {
				t.Errorf("summary %q does not contain %q", check.summary, tt.wantSubstr)
			}
			if tt.wantAdvice != "" && !strings.Contains(check.advice, tt.wantAdvice) {
				t.Errorf("advice %q does not contain %q", check.advice, tt.wantAdvice)
			}
		})
	}

	// Live environment check: verify against linked runtime without stubs.
	liveCheck := nativeRuntimeCheck(transcribe.ModelParakeet06BV3Int8)
	if liveCheck.status == doctorFail {
		t.Fatalf("live nativeRuntimeCheck returned doctorFail: %+v", liveCheck)
	}
	if liveCheck.status != doctorOK && liveCheck.status != doctorWarn {
		t.Fatalf("live nativeRuntimeCheck returned unexpected status: %s", liveCheck.status)
	}
	if !strings.Contains(liveCheck.summary, "speech engine runtime") {
		t.Fatalf("live summary missing speech engine runtime prefix: %s", liveCheck.summary)
	}
}
