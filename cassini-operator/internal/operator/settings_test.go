package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	s := STTSettings{Quality: sttQualityBalanced, DeviceOverride: "cuda", ModelOverride: "x"}
	env := s.ChildEnv(base)

	if m, ok := envValue(env, envSTTModel); !ok || m != "x" {
		t.Fatalf("CASSINI_STT_MODEL = %q ok=%v, want x", m, ok)
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
		ModelOverride:       "custom",
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
	if resp.Effective.Device == "" {
		t.Fatalf("effective.device should be set, got empty: %s", rec.Body.String())
	}
	if resp.Effective.Quality != normalizeQuality(resp.Quality) {
		t.Fatalf("effective.quality = %q, want %q", resp.Effective.Quality, normalizeQuality(resp.Quality))
	}
}
