package operator

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):], true
		}
	}
	return "", false
}

func TestChildEnvStripsImageModelPinAndStaleQuality(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"CASSINI_STT_MODEL=parakeet-tdt-0.6b-v3-int8",
		"CASSINI_STT_QUALITY=fast",
		"CASSINI_STT_NUM_THREADS=8",
		"CASSINI_STT_DEVICE=cpu",
		"CASSINI_STT_ADDITIONAL_MODELS=parakeet-tdt-0.6b-v3-int8",
		"CASSINI_TRANSCRIPTION_TERMS=[\"stale\"]",
	}
	s := STTSettings{Quality: sttQualityBest}
	env := s.ChildEnv(base)

	if q, ok := envValue(env, envSTTQuality); !ok || q != sttQualityBest {
		t.Fatalf("CASSINI_STT_QUALITY = %q ok=%v, want best", q, ok)
	}
	if _, ok := envValue(env, envSTTModel); ok {
		t.Fatalf("CASSINI_STT_MODEL must be stripped when no model override; env=%v", env)
	}
	if _, ok := envValue(env, envSTTDevice); ok {
		t.Fatalf("CASSINI_STT_DEVICE must be stripped when no device override; env=%v", env)
	}
	if _, ok := envValue(env, envSTTNumThreads); ok {
		t.Fatalf("CASSINI_STT_NUM_THREADS must be stripped; env=%v", env)
	}
	if _, ok := envValue(env, envSTTAdditionalModels); ok {
		t.Fatalf("CASSINI_STT_ADDITIONAL_MODELS must be stripped by GPU-only policy; env=%v", env)
	}
	if _, ok := envValue(env, "CASSINI_TRANSCRIPTION_TERMS"); ok {
		t.Fatalf("retired CASSINI_TRANSCRIPTION_TERMS must be stripped; env=%v", env)
	}
	// No stale duplicate quality.
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, envSTTQuality+"=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one CASSINI_STT_QUALITY entry, got %d: %v", count, env)
	}
	// Unrelated env preserved.
	if p, ok := envValue(env, "PATH"); !ok || p != "/usr/bin" {
		t.Fatalf("PATH = %q ok=%v, want /usr/bin", p, ok)
	}
}

func TestChildEnvOverridesAreAppended(t *testing.T) {
	base := []string{
		"CASSINI_STT_MODEL=parakeet-tdt-0.6b-v3-int8",
		"CASSINI_STT_DEVICE=cpu",
	}
	s := STTSettings{Quality: sttQualityBalanced, DeviceOverride: "cuda", ModelOverride: auditedCUDAParakeetV3}
	env := s.ChildEnv(base)

	if m, ok := envValue(env, envSTTModel); !ok || m != auditedCUDAParakeetV3 {
		t.Fatalf("CASSINI_STT_MODEL = %q ok=%v, want %s", m, ok, auditedCUDAParakeetV3)
	}
	if d, ok := envValue(env, envSTTDevice); !ok || d != "cuda" {
		t.Fatalf("CASSINI_STT_DEVICE = %q ok=%v, want cuda", d, ok)
	}
	// Exactly one model/device entry (the inherited ones were stripped).
	for _, key := range []string{envSTTModel, envSTTDevice} {
		count := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected exactly one %s entry, got %d: %v", key, count, env)
		}
	}
}

func TestChildEnvFailsSafeForUnauditedModelOverride(t *testing.T) {
	env := (STTSettings{
		Quality:       sttQualityBest,
		ModelOverride: "parakeet-tdt-0.6b-v3-int8",
	}).ChildEnv([]string{
		"CASSINI_STT_MODEL=unsafe-inherited",
		"CASSINI_STT_ADDITIONAL_MODELS=also-unsafe",
	})
	if _, ok := envValue(env, envSTTModel); ok {
		t.Fatalf("unaudited model override escaped into child environment: %v", env)
	}
	if _, ok := envValue(env, envSTTAdditionalModels); ok {
		t.Fatalf("additional models escaped into child environment: %v", env)
	}
}

func TestValidCUDAModelOverrideAllowlist(t *testing.T) {
	for _, model := range []string{"", "  ", auditedCUDAParakeetV3, "  " + auditedCUDAParakeetV3 + "  "} {
		if !validCUDAModelOverride(model) {
			t.Errorf("audited CUDA model %q was rejected", model)
		}
	}
	for _, model := range []string{"parakeet-tdt-0.6b-v3-int8", "custom", "PARAKEET-TDT-0.6B-V3"} {
		if validCUDAModelOverride(model) {
			t.Errorf("unaudited model %q was accepted", model)
		}
	}
}

func TestNormalizeQuality(t *testing.T) {
	cases := map[string]string{
		"fast":      sttQualityFast,
		"balanced":  sttQualityBalanced,
		"best":      sttQualityBest,
		"BEST":      sttQualityBest,
		"  Fast  ":  sttQualityFast,
		"":          sttQualityBalanced,
		"nonsense":  sttQualityBalanced,
		"ultra":     sttQualityBalanced,
		"BaLaNcEd ": sttQualityBalanced,
	}
	for in, want := range cases {
		if got := normalizeQuality(in); got != want {
			t.Errorf("normalizeQuality(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadOrInitSettingsFirstStartWritesAutoDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	s, err := LoadOrInitSettings(path)
	if err != nil {
		t.Fatalf("LoadOrInitSettings() error = %v", err)
	}
	if s.Source != sttSourceAuto {
		t.Fatalf("Source = %q, want auto", s.Source)
	}
	switch s.Quality {
	case sttQualityFast, sttQualityBalanced, sttQualityBest:
	default:
		t.Fatalf("Quality = %q, want a valid tier", s.Quality)
	}
	if s.HardwareFingerprint == "" {
		t.Fatalf("HardwareFingerprint should be set")
	}
	if s.Cores < 1 {
		t.Fatalf("Cores = %d, want >= 1", s.Cores)
	}

	// File exists and round-trips.
	loaded, err := LoadOrInitSettings(path)
	if err != nil {
		t.Fatalf("second LoadOrInitSettings() error = %v", err)
	}
	if loaded.Source != s.Source || loaded.Quality != s.Quality || loaded.HardwareFingerprint != s.HardwareFingerprint {
		t.Fatalf("round-trip mismatch: %#v vs %#v", loaded, s)
	}
}

func TestSaveLoadRoundTripUserSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := STTSettings{
		Quality:             sttQualityFast,
		DeviceOverride:      "cuda",
		ModelOverride:       auditedCUDAParakeetV3,
		Source:              sttSourceUser,
		HardwareFingerprint: "gpu=true;cores=16",
		DetectedGPU:         true,
		Cores:               16,
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := LoadOrInitSettings(path)
	if err != nil {
		t.Fatalf("LoadOrInitSettings() error = %v", err)
	}
	if got.Source != sttSourceUser {
		t.Fatalf("Source = %q, want user (must not be overwritten)", got.Source)
	}
	if got.Quality != want.Quality || got.DeviceOverride != want.DeviceOverride || got.ModelOverride != want.ModelOverride {
		t.Fatalf("user policy not preserved: got %#v want %#v", got, want)
	}
}

func TestSaveRejectsUnsupportedOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, STTSettings{Quality: sttQualityBest, ModelOverride: "custom"}); err == nil || !strings.Contains(err.Error(), "not an audited CUDA model") {
		t.Fatalf("Save() error = %v, want unaudited CUDA rejection", err)
	}
	for _, device := range []string{"cpu", "tpu"} {
		if err := Save(path, STTSettings{Quality: sttQualityBest, DeviceOverride: device}); err == nil || !strings.Contains(err.Error(), "GPU-only") {
			t.Errorf("Save(device_override=%q) error = %v, want GPU-only rejection", device, err)
		}
	}
}

func TestLoadLegacySettingsHealsUnsupportedOverrides(t *testing.T) {
	for _, tc := range []struct {
		name        string
		device      string
		model       string
		wantDevice  string
		wantModel   string
		messageBits []string
	}{
		{
			name:       "cpu device and unaudited model",
			device:     "cpu",
			model:      "parakeet-tdt-0.6b-v3-int8",
			wantDevice: "",
			wantModel:  "",
			messageBits: []string{
				`unsupported device_override="cpu"`,
				`unaudited model_override="parakeet-tdt-0.6b-v3-int8"`,
			},
		},
		{
			name:       "unknown device",
			device:     "tpu",
			model:      auditedCUDAParakeetV3,
			wantDevice: "",
			wantModel:  auditedCUDAParakeetV3,
			messageBits: []string{
				`unsupported device_override="tpu"`,
			},
		},
		{
			name:       "unaudited model only",
			device:     "cuda",
			model:      "custom/model",
			wantDevice: "cuda",
			wantModel:  "",
			messageBits: []string{
				`unaudited model_override="custom/model"`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			legacy := STTSettings{
				Quality:             sttQualityFast,
				DeviceOverride:      tc.device,
				ModelOverride:       tc.model,
				Source:              sttSourceUser,
				HardwareFingerprint: "legacy-host",
				DetectedGPU:         false,
				Cores:               2,
			}
			raw, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}

			var migrations []SettingsMigration
			got, err := LoadOrInitSettingsWithMigrationReporter(path, func(migration SettingsMigration) {
				migrations = append(migrations, migration)
			})
			if err != nil {
				t.Fatalf("LoadOrInitSettingsWithMigrationReporter() error = %v", err)
			}
			if got.DeviceOverride != tc.wantDevice || got.ModelOverride != tc.wantModel {
				t.Fatalf("healed overrides = device %q model %q, want %q/%q", got.DeviceOverride, got.ModelOverride, tc.wantDevice, tc.wantModel)
			}
			if got.Quality != sttQualityFast || got.Source != sttSourceUser {
				t.Fatalf("legacy quality/source were not preserved: %#v", got)
			}
			if len(migrations) != 1 {
				t.Fatalf("migration reports = %d, want exactly 1", len(migrations))
			}
			message := migrations[0].Message()
			for _, bit := range append(tc.messageBits,
				path,
				`preserved quality="fast" and source="user"`,
				"use Settings to select CUDA",
			) {
				if !strings.Contains(message, bit) {
					t.Errorf("migration message %q does not contain %q", message, bit)
				}
			}

			// Read the file directly: a successful in-memory migration is not
			// enough because it would recur after every operator restart.
			persistedRaw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted STTSettings
			if err := json.Unmarshal(persistedRaw, &persisted); err != nil {
				t.Fatalf("decode healed settings: %v", err)
			}
			if persisted.DeviceOverride != tc.wantDevice || persisted.ModelOverride != tc.wantModel {
				t.Fatalf("persisted overrides = device %q model %q, want %q/%q", persisted.DeviceOverride, persisted.ModelOverride, tc.wantDevice, tc.wantModel)
			}
			if persisted.Quality != sttQualityFast || persisted.Source != sttSourceUser {
				t.Fatalf("persisted policy was not preserved: %#v", persisted)
			}

			migrations = nil
			if _, err := LoadOrInitSettingsWithMigrationReporter(path, func(migration SettingsMigration) {
				migrations = append(migrations, migration)
			}); err != nil {
				t.Fatalf("reload healed settings: %v", err)
			}
			if len(migrations) != 0 {
				t.Fatalf("healed file migrated again: %#v", migrations)
			}
		})
	}
}

func TestPutSettingsValidPersistsUserSource(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	body := `{"quality":"best","device_override":"cuda"}`
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /settings = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Quality != sttQualityBest {
		t.Fatalf("Quality = %q, want best", resp.Quality)
	}
	if resp.Source != sttSourceUser {
		t.Fatalf("Source = %q, want user", resp.Source)
	}
	if resp.DeviceOverride != "cuda" {
		t.Fatalf("DeviceOverride = %q, want cuda", resp.DeviceOverride)
	}
	if resp.Effective.Device != "cuda" {
		t.Fatalf("Effective.Device = %q, want cuda", resp.Effective.Device)
	}

	// In-memory copy updated so the next spawned job sees it.
	if got := rt.currentSettings(); got.Quality != sttQualityBest || got.Source != sttSourceUser {
		t.Fatalf("in-memory settings not updated: %#v", got)
	}

	// Persisted to disk and survives reload.
	reloaded, err := LoadOrInitSettings(rt.settingsPath)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if reloaded.Quality != sttQualityBest || reloaded.Source != sttSourceUser {
		t.Fatalf("persisted settings mismatch: %#v", reloaded)
	}
}

func TestPutSettingsInvalidQualityRejected(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"ultra"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT invalid quality = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutSettingsInvalidDeviceRejected(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","device_override":"tpu"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT invalid device = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutSettingsRejectsCPUForGPUOnlyOperator(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","device_override":"cpu"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT CPU device = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "GPU-only") {
		t.Fatalf("CPU rejection did not explain GPU-only policy: %s", rec.Body.String())
	}
}

func TestPutSettingsRejectsUnauditedCUDAModel(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","model_override":"parakeet-tdt-0.6b-v3-int8"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT unaudited model = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), auditedCUDAParakeetV3) {
		t.Fatalf("model rejection did not name the audited CUDA model: %s", rec.Body.String())
	}
}

func TestGetSettingsReturnsEffectiveView(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Effective.Device != "cuda" {
		t.Fatalf("effective.device = %q, want forced cuda: %s", resp.Effective.Device, rec.Body.String())
	}
	if resp.Effective.Model != auditedCUDAParakeetV3 {
		t.Fatalf("effective.model = %q, want %s: %s", resp.Effective.Model, auditedCUDAParakeetV3, rec.Body.String())
	}
	if resp.Effective.Quality != normalizeQuality(resp.Quality) {
		t.Fatalf("effective.quality = %q, want %q", resp.Effective.Quality, normalizeQuality(resp.Quality))
	}
}

func TestGetSettingsLogsPersistedPolicyMigration(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	legacy := STTSettings{
		Quality:        sttQualityBest,
		DeviceOverride: "cpu",
		ModelOverride:  "legacy-int8-model",
		Source:         sttSourceUser,
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.settingsPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	rt.logger = log.New(&logs, "operator: ", 0)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	for _, bit := range []string{
		"operator: stt_settings migrated",
		`unsupported device_override="cpu"`,
		`unaudited model_override="legacy-int8-model"`,
		"use Settings to select CUDA",
	} {
		if !strings.Contains(logs.String(), bit) {
			t.Errorf("migration log %q does not contain %q", logs.String(), bit)
		}
	}
}
