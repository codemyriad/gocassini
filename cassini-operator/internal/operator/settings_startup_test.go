package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startRuntimeIn runs NewRuntime over dir, as a restart of the operator on an
// existing volume does.
func startRuntimeIn(t *testing.T, dir string, logger *log.Logger) *Runtime {
	t.Helper()
	t.Setenv(envSTTCUDACapable, "1")
	store, err := OpenStore(filepath.Join(dir, "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	rt := NewRuntime(context.Background(), store, Config{
		RepoRoot:         repoRoot,
		BindAddr:         "127.0.0.1:0",
		DBPath:           filepath.Join(dir, "jobs.sqlite3"),
		WorkRoot:         filepath.Join(dir, "jobs"),
		SiteRoot:         filepath.Join(dir, "site"),
		CassiniBin:       filepath.Join(repoRoot, "bin", "cassini"),
		MaxRecordWorkers: 1,
		MaxBuildWorkers:  1,
		TalkSharedSecret: "test-recording-secret",
	}, logger, ioDiscard{}, ioDiscard{})
	t.Cleanup(func() { cleanupTestRuntime(t, rt, store) })
	return rt
}

func writeSettingsFixture(t *testing.T, path string, s STTSettings) []byte {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return raw
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The legacy-override migration is reported where the file is loaded: at
// start, not when Settings is opened.
func TestStartLogsPersistedPolicyMigration(t *testing.T) {
	clearLLMEnv(t)
	dir := t.TempDir()
	writeSettingsFixture(t, filepath.Join(dir, "settings.json"), STTSettings{
		Quality:            sttQualityBest,
		DeviceOverride:     "tpu",
		TranscriptionTerms: []string{"Gocassini"},
		Source:             sttSourceUser,
	})
	var logs bytes.Buffer
	rt := startRuntimeIn(t, dir, log.New(&logs, "operator: ", 0))
	for _, bit := range []string{
		"operator: stt_settings migrated",
		`unsupported device_override="tpu"`,
		"the policy now auto-selects the device",
	} {
		if !strings.Contains(logs.String(), bit) {
			t.Errorf("migration log %q does not contain %q", logs.String(), bit)
		}
	}
	if got := rt.currentSettings(); got.DeviceOverride != "" || got.Quality != sttQualityBest {
		t.Fatalf("migrated policy = %+v", got)
	}
	var saved STTSettings
	if err := json.Unmarshal(readFileBytes(t, rt.settingsPath), &saved); err != nil || saved.DeviceOverride != "" {
		t.Fatalf("migration not saved: %+v, %v", saved, err)
	}
}

// A GET serves memory: deleting or truncating both files while Cassini runs
// neither replaces the running policy nor recreates or rewrites a file.
func TestGetSettingsIsAPureRead(t *testing.T) {
	clearLLMEnv(t)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"share_audience":"moderators","transcription_terms":["Gocassini"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /settings = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(`{"providers":[{"id":"p","base_url":"http://llm.test/v1","api_key":"k"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /settings/llm = %d %s", rec.Code, rec.Body.String())
	}
	for name, damage := range map[string]func(string){
		"deleted":   func(p string) { _ = os.Remove(p) },
		"truncated": func(p string) { _ = os.WriteFile(p, []byte("{trunc"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			damage(rt.settingsPath)
			damage(rt.llmSettingsPath)
			sttBefore, sttErr := os.ReadFile(rt.settingsPath)
			llmBefore, llmErr := os.ReadFile(rt.llmSettingsPath)

			rec := httptest.NewRecorder()
			rt.settingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
			var got settingsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("GET /settings = %d %s", rec.Code, rec.Body.String())
			}
			if got.ShareAudience != shareAudienceModerators || len(got.TranscriptionTerms) != 1 {
				t.Fatalf("GET replaced the running policy: %+v", got.STTSettings)
			}
			rec = httptest.NewRecorder()
			rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm", nil))
			if out := decodeLLMSettings(t, rec); len(out.Providers) != 1 || out.Providers[0].ID != "p" {
				t.Fatalf("GET /settings/llm replaced the running providers: %+v", out.Providers)
			}

			sttAfter, sttErrAfter := os.ReadFile(rt.settingsPath)
			llmAfter, llmErrAfter := os.ReadFile(rt.llmSettingsPath)
			if !bytes.Equal(sttBefore, sttAfter) || (sttErr == nil) != (sttErrAfter == nil) {
				t.Fatalf("GET touched settings.json: %q -> %q", sttBefore, sttAfter)
			}
			if !bytes.Equal(llmBefore, llmAfter) || (llmErr == nil) != (llmErrAfter == nil) {
				t.Fatalf("GET touched llm-settings.json: %q -> %q", llmBefore, llmAfter)
			}
		})
	}
}

// Loading never writes for a hardware change, under either source, and keeps
// the device pin of an automatic policy.
func TestLoadNeverWritesForAHardwareChange(t *testing.T) {
	t.Setenv(envSTTCUDACapable, "1")
	cases := []struct {
		name       string
		stored     STTSettings
		gpu        bool
		wantDevice string
		wantQual   string
	}{
		{
			name:       "auto + cpu pin + stale fingerprint keeps the pin",
			stored:     STTSettings{Source: sttSourceAuto, Quality: sttQualityBalanced, DeviceOverride: deviceCPU},
			gpu:        false,
			wantDevice: deviceCPU,
			wantQual:   sttQualityBalanced,
		},
		{
			// The automatic tier is today's formula, not an override-aware one:
			// a stored best must not flip to balanced and cause 409s.
			name:       "auto + cpu pin on a CUDA host keeps best",
			stored:     STTSettings{Source: sttSourceAuto, Quality: sttQualityBest, DeviceOverride: deviceCPU},
			gpu:        true,
			wantDevice: deviceCPU,
			wantQual:   sttQualityBest,
		},
		{
			// Decision O2: ignored for this start only; the file keeps it.
			name:       "auto + cuda pin without a GPU is cleared in memory only",
			stored:     STTSettings{Source: sttSourceAuto, Quality: sttQualityBest, DeviceOverride: deviceCUDA},
			gpu:        false,
			wantDevice: "",
			wantQual:   sttQualityBalanced,
		},
		{
			name:       "auto + cuda pin with a GPU keeps it",
			stored:     STTSettings{Source: sttSourceAuto, Quality: sttQualityBest, DeviceOverride: deviceCUDA},
			gpu:        true,
			wantDevice: deviceCUDA,
			wantQual:   sttQualityBest,
		},
		{
			name:       "user source shows live hardware and keeps its policy",
			stored:     STTSettings{Source: sttSourceUser, Quality: sttQualityFast, DeviceOverride: deviceCUDA},
			gpu:        false,
			wantDevice: deviceCUDA,
			wantQual:   sttQualityFast,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubNVIDIADevice(t, tc.gpu)
			path := filepath.Join(t.TempDir(), "settings.json")
			stored := tc.stored
			stored.TranscriptionEnabled = true
			stored.ShareAudience = shareAudienceModerators
			stored.TranscriptionTerms = []string{"Gocassini"}
			stored.HardwareFingerprint = "gpu=false;cuda=false;cores=1"
			stored.Cores = 1
			before := writeSettingsFixture(t, path, stored)

			var migrations []SettingsMigration
			got, err := LoadOrInitSettingsWithMigrationReporter(path, func(m SettingsMigration) { migrations = append(migrations, m) })
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, readFileBytes(t, path)) {
				t.Fatal("loading rewrote settings.json")
			}
			if len(migrations) != 0 {
				t.Fatalf("migrations = %+v, want none", migrations)
			}
			if got.DeviceOverride != tc.wantDevice || got.Quality != tc.wantQual || got.Source != tc.stored.Source {
				t.Fatalf("policy = device %q quality %q source %q, want %q %q %q", got.DeviceOverride, got.Quality, got.Source, tc.wantDevice, tc.wantQual, tc.stored.Source)
			}
			if !got.TranscriptionEnabled || got.ShareAudience != shareAudienceModerators || len(got.TranscriptionTerms) != 1 {
				t.Fatalf("loading changed the rest of the policy: %+v", got)
			}
			// DetectedGPU is raw device-node visibility, not the stub.
			live := detectGPU()
			if got.HardwareFingerprint != hardwareFingerprint(live, got.Cores) || got.DetectedGPU != live || got.Cores < 1 {
				t.Fatalf("display fields not refreshed: %+v", got)
			}
		})
	}
}

// A failed write at start never discards a parsed policy: on a read-only
// directory a stale fingerprint, and even the legacy-override migration, load
// with transcription, audience and retention intact.
func TestLoadOnAReadOnlyDirectoryKeepsThePolicy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	for _, override := range []string{deviceCPU, "tpu"} {
		t.Run(override, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			before := writeSettingsFixture(t, path, STTSettings{
				Source:               sttSourceAuto,
				Quality:              sttQualityBalanced,
				DeviceOverride:       override,
				TranscriptionEnabled: true,
				ActiveModel:          "m",
				ActiveRevision:       "r",
				MeetingFormat:        "json",
				SourceRetention:      sourceDeleteAfterProcessing,
				ShareAudience:        shareAudienceAttendees,
				HardwareFingerprint:  "previous-host",
			})
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

			var migrations []SettingsMigration
			got, err := LoadOrInitSettingsWithMigrationReporter(path, func(m SettingsMigration) { migrations = append(migrations, m) })
			if err != nil {
				t.Fatalf("load on a read-only directory failed: %v", err)
			}
			if !got.TranscriptionEnabled || got.ShareAudience != shareAudienceAttendees || got.SourceRetention != sourceDeleteAfterProcessing {
				t.Fatalf("policy lost: %+v", got)
			}
			if !bytes.Equal(before, readFileBytes(t, path)) {
				t.Fatal("settings.json changed")
			}
			if override == deviceCPU {
				if got.DeviceOverride != deviceCPU || len(migrations) != 0 {
					t.Fatalf("cpu pin: device %q, migrations %+v", got.DeviceOverride, migrations)
				}
				return
			}
			if got.DeviceOverride != "" || len(migrations) != 1 || migrations[0].SaveError == "" {
				t.Fatalf("legacy override: device %q, migrations %+v", got.DeviceOverride, migrations)
			}
			if msg := migrations[0].Message(); !strings.Contains(msg, "could not be saved") || !strings.Contains(msg, "next start clears it again") {
				t.Fatalf("migration message = %q", msg)
			}
		})
	}
}

// A file Cassini cannot load at start is kept once, as a hard link with its
// own bytes and mode, before a save can replace it; the save is not refused.
// The running defaults never widen who sees a recording (decision O7).
func TestRejectedSettingsFileIsKeptAndSaveStillWorks(t *testing.T) {
	clearLLMEnv(t)
	dir := t.TempDir()
	broken := []byte(`{"share_audience":"moderators","transcription_enabled":tr`)
	brokenLLM := []byte(`{"providers":[{"id":"x","api_key":"sk`)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llm-settings.json"), brokenLLM, 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	rt := startRuntimeIn(t, dir, log.New(&logs, "", 0))
	for _, bit := range []string{
		"stt_settings could not be loaded",
		"running on defaults (transcription off, new recordings shared with attendees only)",
		"The file is kept as settings.json.rejected-",
		"move that file back over settings.json and restart",
		"llm_settings could not be loaded",
		"The file is kept as llm-settings.json.rejected-",
	} {
		if !strings.Contains(logs.String(), bit) {
			t.Errorf("log %q does not contain %q", logs.String(), bit)
		}
	}
	if got := rt.currentSettings(); got.ShareAudience != shareAudienceAttendees || got.TranscriptionEnabled {
		t.Fatalf("fallback policy = %+v, want attendees with transcription off", got)
	}

	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"meeting_format":"opus"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT refused: %d %s", rec.Code, rec.Body.String())
	}
	copies, _ := filepath.Glob(filepath.Join(dir, "settings.json.rejected-*"))
	if len(copies) != 1 {
		t.Fatalf("copies = %v", copies)
	}
	if kept := readFileBytes(t, copies[0]); !bytes.Equal(kept, broken) {
		t.Fatalf("kept %q, want the rejected bytes", kept)
	}
	llmCopies, _ := filepath.Glob(filepath.Join(dir, "llm-settings.json.rejected-*"))
	if len(llmCopies) != 1 {
		t.Fatalf("llm copies = %v", llmCopies)
	}
	if info, err := os.Stat(llmCopies[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("llm copy mode = %v, %v", info, err)
	}

	// A second start on the same rejected LLM file keeps no second copy.
	_ = startRuntimeIn(t, dir, log.New(&bytes.Buffer{}, "", 0))
	if again, _ := filepath.Glob(filepath.Join(dir, "llm-settings.json.rejected-*")); len(again) != 1 {
		t.Fatalf("restart duplicated the copy: %v", again)
	}
}

// A fresh install keeps the room default, and one whose LLM environment does
// not validate keeps no copy and can still be fixed from AI providers.
func TestFreshInstallMakesNoRejectedCopy(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv(envLLMBaseURL, "https://x.test/v1/chat/completions")
	dir := t.TempDir()
	var logs bytes.Buffer
	rt := startRuntimeIn(t, dir, log.New(&logs, "", 0))
	if strings.Contains(logs.String(), "rejected") || strings.Contains(logs.String(), "stt_settings could not be loaded") {
		t.Fatalf("fresh install log: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "the AI endpoint from the deploy environment cannot be saved") ||
		!strings.Contains(logs.String(), "Restart to retry; saving AI providers now writes a new llm-settings.json.") {
		t.Fatalf("fresh install with a bad LLM env logged %q", logs.String())
	}
	if got := rt.currentSettings(); got.ShareAudience != shareAudienceRoom {
		t.Fatalf("fresh install audience = %q, want room", got.ShareAudience)
	}
	if _, err := os.Stat(rt.settingsPath); !os.IsNotExist(err) {
		t.Fatalf("start created settings.json: %v", err)
	}
	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings/llm = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(`{"providers":[{"id":"default","base_url":"https://x.test/v1"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("corrective PUT = %d %s", rec.Code, rec.Body.String())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.rejected-*")); len(m) != 0 {
		t.Fatalf("copies on a fresh install: %v", m)
	}
}
