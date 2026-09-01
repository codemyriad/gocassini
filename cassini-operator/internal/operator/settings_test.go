package operator

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	s := STTSettings{
		Quality:            sttQualityBest,
		TranscriptionTerms: []string{" Gocassini ", "Nextcloud Talk", "gocassini"},
	}
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
	termsJSON, ok := envValue(env, envTranscriptionTerms)
	if !ok {
		t.Fatalf("CASSINI_TRANSCRIPTION_TERMS must be set; env=%v", env)
	}
	var terms []string
	if err := json.Unmarshal([]byte(termsJSON), &terms); err != nil {
		t.Fatalf("decode CASSINI_TRANSCRIPTION_TERMS: %v", err)
	}
	if got := strings.Join(terms, "|"); got != "Gocassini|Nextcloud Talk" {
		t.Fatalf("CASSINI_TRANSCRIPTION_TERMS = %q, want normalized preferred spellings", got)
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
	s := STTSettings{Quality: sttQualityBalanced, DeviceOverride: "cuda", ModelOverride: modelParakeetV3Fp32}
	env := s.ChildEnv(base)

	if m, ok := envValue(env, envSTTModel); !ok || m != modelParakeetV3Fp32 {
		t.Fatalf("CASSINI_STT_MODEL = %q ok=%v, want %s", m, ok, modelParakeetV3Fp32)
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
		ModelOverride: "custom/model",
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

func TestValidModelOverrideAllowlist(t *testing.T) {
	// Every model a quality tier can select is audited: with CPU builds
	// supported, pinning the int8 or 110M model is as legitimate as pinning
	// fp32 (D-702). Anything outside the tier set stays rejected.
	audited := []string{"", "  "}
	for _, model := range auditedModels() {
		audited = append(audited, model, "  "+model+"  ")
	}
	for _, model := range audited {
		if !validModelOverride(model) {
			t.Errorf("audited model %q was rejected", model)
		}
	}
	for _, model := range []string{"custom", "PARAKEET-TDT-0.6B-V3", "parakeet-tdt-0.6b-v2-int8"} {
		if validModelOverride(model) {
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

func TestNormalizeTranscriptionTerms(t *testing.T) {
	terms, err := normalizeTranscriptionTerms([]string{
		"  Gocassini  ",
		"Nextcloud\t Talk",
		"gocassini",
		"",
		"  ",
	})
	if err != nil {
		t.Fatalf("normalizeTranscriptionTerms() error = %v", err)
	}
	if got := strings.Join(terms, "|"); got != "Gocassini|Nextcloud Talk" {
		t.Fatalf("normalized terms = %q, want Gocassini|Nextcloud Talk", got)
	}
}

func TestNormalizeTranscriptionTermsEnforcesBounds(t *testing.T) {
	tooLong := strings.Repeat("x", maxTranscriptionTermRunes+1)
	if _, err := normalizeTranscriptionTerms([]string{tooLong}); err == nil {
		t.Fatal("expected overlong term to be rejected")
	}

	tooMany := make([]string, maxTranscriptionTerms+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("term-%d", i)
	}
	if _, err := normalizeTranscriptionTerms(tooMany); err == nil {
		t.Fatal("expected excess terms to be rejected")
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
		ModelOverride:       modelParakeetV3Fp32,
		TranscriptionTerms:  []string{"Gocassini", "Nextcloud Talk"},
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
	if got.Quality != want.Quality || got.DeviceOverride != want.DeviceOverride || got.ModelOverride != want.ModelOverride || strings.Join(got.TranscriptionTerms, "|") != strings.Join(want.TranscriptionTerms, "|") {
		t.Fatalf("user policy not preserved: got %#v want %#v", got, want)
	}
}

func TestSaveRejectsUnsupportedOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, STTSettings{Quality: sttQualityBest, ModelOverride: "custom"}); err == nil || !strings.Contains(err.Error(), "not an audited model") {
		t.Fatalf("Save() error = %v, want unaudited model rejection", err)
	}
	if err := Save(path, STTSettings{Quality: sttQualityBest, DeviceOverride: "tpu"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("Save(device_override=\"tpu\") error = %v, want rejection", err)
	}
	// cpu is a device the operator can execute, so it must persist rather than
	// be rejected as it was under the GPU-only policy (D-702).
	for _, device := range []string{deviceCPU, deviceCUDA} {
		if err := Save(path, STTSettings{Quality: sttQualityBest, DeviceOverride: device, Source: sttSourceUser}); err != nil {
			t.Errorf("Save(device_override=%q) error = %v, want accepted", device, err)
		}
		loaded, err := LoadOrInitSettings(path)
		if err != nil {
			t.Fatalf("LoadOrInitSettings() error = %v", err)
		}
		if loaded.DeviceOverride != device {
			t.Errorf("device_override = %q, want %q preserved", loaded.DeviceOverride, device)
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
			// cpu survives the migration untouched — only the model is healed.
			name:       "supported cpu device with an unaudited model",
			device:     "cpu",
			model:      "custom/model",
			wantDevice: "cpu",
			wantModel:  "",
			messageBits: []string{
				`unaudited model_override="custom/model"`,
			},
		},
		{
			name:       "unknown device",
			device:     "tpu",
			model:      modelParakeetV3Fp32,
			wantDevice: "",
			wantModel:  modelParakeetV3Fp32,
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
				TranscriptionTerms:  []string{" Gocassini ", "Nextcloud\tTalk", "gocassini"},
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
			if terms := strings.Join(got.TranscriptionTerms, "|"); terms != "Gocassini|Nextcloud Talk" {
				t.Fatalf("legacy transcription terms = %q, want normalized preserved terms", terms)
			}
			if len(migrations) != 1 {
				t.Fatalf("migration reports = %d, want exactly 1", len(migrations))
			}
			message := migrations[0].Message()
			for _, bit := range append(tc.messageBits,
				path,
				`preserved quality="fast", source="user", and 2 transcription terms`,
				"the policy now auto-selects the device",
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
			if persisted.Quality != sttQualityFast || persisted.Source != sttSourceUser || strings.Join(persisted.TranscriptionTerms, "|") != "Gocassini|Nextcloud Talk" {
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

	body := `{"quality":"best","device_override":"cuda","transcription_terms":[" Gocassini ","Nextcloud Talk","gocassini"]}`
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
	if got := strings.Join(resp.TranscriptionTerms, "|"); got != "Gocassini|Nextcloud Talk" {
		t.Fatalf("TranscriptionTerms = %q, want normalized preferred spellings", got)
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
	if reloaded.Quality != sttQualityBest || reloaded.Source != sttSourceUser || strings.Join(reloaded.TranscriptionTerms, "|") != "Gocassini|Nextcloud Talk" {
		t.Fatalf("persisted settings mismatch: %#v", reloaded)
	}
}

func TestPutSettingsRejectsOutOfBoundsTranscriptionTerms(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	body, err := json.Marshal(map[string]any{
		"quality":             "best",
		"transcription_terms": []string{strings.Repeat("x", maxTranscriptionTermRunes+1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT overlong transcription term = %d, want 400 body=%s", rec.Code, rec.Body.String())
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

func TestPutSettingsAcceptsCPUOverride(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","device_override":"cpu"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT CPU device = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DeviceOverride != deviceCPU || resp.Effective.Device != deviceCPU {
		t.Fatalf("device_override = %q effective.device = %q, want both cpu: %s",
			resp.DeviceOverride, resp.Effective.Device, rec.Body.String())
	}
	// best on CPU is fp32 — the tiers mean something again once builds are not
	// forced onto CUDA.
	if resp.Effective.Model != modelParakeetV3Fp32 {
		t.Fatalf("effective.model = %q, want %s", resp.Effective.Model, modelParakeetV3Fp32)
	}
	persisted, err := LoadOrInitSettings(rt.settingsPath)
	if err != nil {
		t.Fatalf("LoadOrInitSettings() error = %v", err)
	}
	if persisted.DeviceOverride != deviceCPU {
		t.Fatalf("persisted device_override = %q, want cpu", persisted.DeviceOverride)
	}
}

func TestPutSettingsRejectsUnauditedCUDAModel(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","model_override":"custom/model"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT unaudited model = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	for _, model := range auditedModels() {
		if !strings.Contains(rec.Body.String(), model) {
			t.Fatalf("model rejection did not name the audited model %s: %s", model, rec.Body.String())
		}
	}
}

func TestGetSettingsReturnsEffectiveView(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	// After newTestRuntime, which declares CUDA capability itself: setting this
	// first would be silently overwritten, and the test would then assert the
	// CPU path while a GPU host resolved CUDA. Stub the device too so the
	// assertion does not depend on the host at all.
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)

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
	// A portable image reports the device it will really use — the CPU — and
	// the model that tier loads there, instead of claiming a forced CUDA run
	// the host cannot perform.
	if resp.Effective.Device != deviceCPU {
		t.Fatalf("effective.device = %q, want cpu: %s", resp.Effective.Device, rec.Body.String())
	}
	if resp.Effective.Model != modelForQuality(resp.Quality, deviceCPU) {
		t.Fatalf("effective.model = %q, want %s: %s", resp.Effective.Model, modelForQuality(resp.Quality, deviceCPU), rec.Body.String())
	}
	if resp.Effective.Note == "" {
		t.Error("effective.note is empty; the panel has nothing to explain the device with")
	}
	if resp.Effective.Quality != normalizeQuality(resp.Quality) {
		t.Fatalf("effective.quality = %q, want %q", resp.Effective.Quality, normalizeQuality(resp.Quality))
	}
}

func TestGetSettingsLogsPersistedPolicyMigration(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	legacy := STTSettings{
		Quality:            sttQualityBest,
		DeviceOverride:     "tpu",
		ModelOverride:      "legacy-int8-model",
		TranscriptionTerms: []string{"Gocassini"},
		Source:             sttSourceUser,
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
		`unsupported device_override="tpu"`,
		`unaudited model_override="legacy-int8-model"`,
		"the policy now auto-selects the device",
	} {
		if !strings.Contains(logs.String(), bit) {
			t.Errorf("migration log %q does not contain %q", logs.String(), bit)
		}
	}
}

func TestLoadPreservesStoredCPUOverrideWithoutMigrating(t *testing.T) {
	// A pinned CPU device is a supported policy, not a legacy value to heal:
	// clearing it (as the GPU-only loader did) silently changed what the
	// administrator asked for on every restart.
	path := filepath.Join(t.TempDir(), "settings.json")
	stored := STTSettings{
		Quality:        sttQualityFast,
		DeviceOverride: deviceCPU,
		Source:         sttSourceUser,
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var migrations []SettingsMigration
	got, err := LoadOrInitSettingsWithMigrationReporter(path, func(m SettingsMigration) {
		migrations = append(migrations, m)
	})
	if err != nil {
		t.Fatalf("LoadOrInitSettingsWithMigrationReporter() error = %v", err)
	}
	if got.DeviceOverride != deviceCPU {
		t.Errorf("device_override = %q, want cpu preserved", got.DeviceOverride)
	}
	if len(migrations) != 0 {
		t.Errorf("migrations = %d (%v), want none for a supported policy", len(migrations), migrations)
	}
}

func TestEffectiveDeviceReportsThePinnedDevice(t *testing.T) {
	t.Setenv(envSTTCUDACapable, "0")
	if device, note := effectiveDevice(deviceCPU); device != deviceCPU || note == "" {
		t.Errorf("effectiveDevice(cpu) = %q/%q, want cpu with a note", device, note)
	}
	// An unsatisfiable CUDA pin still reports cuda: the administrator's intent
	// is what /status then explains as unusable, rather than a silent CPU swap.
	device, note := effectiveDevice(deviceCUDA)
	if device != deviceCUDA {
		t.Errorf("effectiveDevice(cuda) = %q, want cuda", device)
	}
	if !strings.Contains(note, "no usable CUDA runtime") {
		t.Errorf("effectiveDevice(cuda) note = %q, want an explanation of why it cannot run", note)
	}
	if device, _ := effectiveDevice(""); device != deviceCPU {
		t.Errorf("effectiveDevice(auto) on a portable image = %q, want cpu", device)
	}
}

func TestPutSettingsRejectsACPUModelPinnedToCUDA(t *testing.T) {
	// Saving the pair and failing at build time would strand a recording; the
	// combination can never run, so refuse it at the API (D-702).
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/settings",
		strings.NewReader(`{"quality":"balanced","device_override":"cuda","model_override":"parakeet-tdt-0.6b-v3-int8"}`))
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT int8 model + cuda = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), modelParakeetV3Fp32) {
		t.Errorf("rejection does not name the model that does run on CUDA: %s", rec.Body.String())
	}

	// The same model without the CUDA pin is fine.
	req = httptest.NewRequest(http.MethodPut, "/settings",
		strings.NewReader(`{"quality":"balanced","model_override":"parakeet-tdt-0.6b-v3-int8"}`))
	rec = httptest.NewRecorder()
	rt.settingsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT int8 model on auto = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEffectiveExplainsAnUnrunnableModelDevicePair(t *testing.T) {
	effective := STTSettings{
		Quality:        sttQualityBalanced,
		DeviceOverride: deviceCUDA,
		ModelOverride:  modelParakeetV3Int8,
	}.effective()
	if effective.Device != deviceCUDA || effective.Model != modelParakeetV3Int8 {
		t.Fatalf("effective = %+v, want the administrator's own pins reported back", effective)
	}
	if !strings.Contains(effective.Note, "cannot run on CUDA") {
		t.Errorf("note %q does not explain why builds will block", effective.Note)
	}
}

func TestAutoQualityFollowsEffectiveCUDACapability(t *testing.T) {
	// The plain image on a GPU daemon sees /dev/nvidia* but transcribes on the
	// CPU. Defaulting it to "best" would pick the fp32 tier — 1.4x realtime and
	// a 4.5GiB floor — for a host that will never touch the GPU.
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, true)
	if got := detectSettings().Quality; got != sttQualityBalanced {
		t.Errorf("auto quality on a portable image with a visible GPU = %q, want balanced", got)
	}

	t.Setenv(envSTTCUDACapable, "1")
	if got := detectSettings().Quality; got != sttQualityBest {
		t.Errorf("auto quality on a CUDA-capable host = %q, want best", got)
	}

	// The fingerprint must move when capability changes on unchanged hardware,
	// or an auto policy would never re-derive after a -cuda image swap.
	capable := hardwareFingerprint(true, 8)
	t.Setenv(envSTTCUDACapable, "0")
	if incapable := hardwareFingerprint(true, 8); incapable == capable {
		t.Errorf("fingerprint %q does not distinguish a CUDA-capable image from a portable one", capable)
	}
}

func TestGetSettingsReportsTheModelABuildWouldLoad(t *testing.T) {
	// An image that does not carry the tier's model runs a bundled one for an
	// automatic policy. The panel reads /settings, so it must name that model —
	// otherwise the two endpoints an administrator can consult disagree about
	// what is running.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	cacheRoot := t.TempDir()
	rt.cfg.ModelCacheRoot = cacheRoot
	rt.cfg.DisallowModelDownload = true
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	bundled := filepath.Join(cacheRoot, "models", modelParakeetV3Fp32)
	if err := os.MkdirAll(bundled, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundled, "encoder.onnx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt.setSettings(STTSettings{Quality: sttQualityBalanced, Source: sttSourceAuto})

	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Effective.Model != modelParakeetV3Fp32 {
		t.Fatalf("effective.model = %q, want the bundled %s the build will load", resp.Effective.Model, modelParakeetV3Fp32)
	}
}
