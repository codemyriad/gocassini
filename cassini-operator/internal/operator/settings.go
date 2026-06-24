package operator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// STTSettings is the operator-owned, persisted speech-to-text policy. The
// operator detects the host hardware on first start and writes an auto default;
// the admin UI can later pin a quality tier (and optional device/model
// overrides) which the operator injects into every record/build/doctor
// subprocess. This is the authoritative source for STT policy: it overrides the
// image's baked CASSINI_STT_* env (the deployed image pins
// CASSINI_STT_MODEL=int8, which would otherwise shadow the chosen tier) so the
// recorder's auto-detect + tier resolution (D-434) actually runs (D-435).
type STTSettings struct {
	Quality             string `json:"quality"`
	DeviceOverride      string `json:"device_override,omitempty"`
	ModelOverride       string `json:"model_override,omitempty"`
	Source              string `json:"source"` // "auto" | "user"
	HardwareFingerprint string `json:"hardware_fingerprint"`
	DetectedGPU         bool   `json:"detected_gpu"`
	Cores               int    `json:"cores"`
}

const (
	sttQualityFast     = "fast"
	sttQualityBalanced = "balanced"
	sttQualityBest     = "best"

	sttSourceAuto = "auto"
	sttSourceUser = "user"

	envSTTQuality    = "CASSINI_STT_QUALITY"
	envSTTNumThreads = "CASSINI_STT_NUM_THREADS"
)

// settingsPath returns the settings.json location: the same persistent dir as
// the sqlite DB, so the config survives restart + redeploy on the AppAPI
// volume.
func settingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), "settings.json")
}

// detectGPU reports whether an NVIDIA GPU device node is visible. It mirrors
// the recorder's `--device auto` detection (NVIDIA device node) so the
// operator's auto default tracks what the recorder will actually use (D-434).
func detectGPU() bool {
	for _, node := range []string{"/dev/nvidia0", "/dev/nvidiactl"} {
		if _, err := os.Stat(node); err == nil {
			return true
		}
	}
	return false
}

// hardwareFingerprint is a short, comparable summary of the host so an auto
// default can detect when the hardware changed (GPU added/removed) across
// restarts and re-derive accordingly.
func hardwareFingerprint(gpu bool, cores int) string {
	return fmt.Sprintf("gpu=%t;cores=%d", gpu, cores)
}

// defaultQualityForHardware picks the auto-default tier: on a GPU fp32 is both
// the quality ceiling and fast, so "best"; on CPU keep "balanced" (int8) which
// preserves the historical CPU speed/quality default.
func defaultQualityForHardware(gpu bool) string {
	if gpu {
		return sttQualityBest
	}
	return sttQualityBalanced
}

// normalizeQuality coerces an arbitrary input to one of fast|balanced|best,
// defaulting to balanced for anything unrecognised.
func normalizeQuality(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case sttQualityFast:
		return sttQualityFast
	case sttQualityBest:
		return sttQualityBest
	case sttQualityBalanced:
		return sttQualityBalanced
	default:
		return sttQualityBalanced
	}
}

// detectSettings builds a fresh auto default from the live host hardware.
func detectSettings() STTSettings {
	gpu := detectGPU()
	cores := runtime.NumCPU()
	return STTSettings{
		Quality:             defaultQualityForHardware(gpu),
		Source:              sttSourceAuto,
		HardwareFingerprint: hardwareFingerprint(gpu, cores),
		DetectedGPU:         gpu,
		Cores:               cores,
	}
}

// LoadOrInitSettings loads settings.json, or — on first start (file missing) —
// detects the hardware, writes an auto default, and returns it.
//
// On a non-first start the live hardware is re-fingerprinted. When the stored
// source is "auto" and the hardware changed (e.g. a GPU was added or removed)
// the auto default is re-derived and rewritten so it tracks the host. When the
// source is "user" the user's Quality/overrides are never touched; only the
// DetectedGPU/Cores/HardwareFingerprint display fields are refreshed (persisted
// best-effort) so the UI shows the current host.
func LoadOrInitSettings(path string) (STTSettings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s := detectSettings()
			if err := Save(path, s); err != nil {
				return STTSettings{}, err
			}
			return s, nil
		}
		return STTSettings{}, fmt.Errorf("read settings: %w", err)
	}

	var s STTSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return STTSettings{}, fmt.Errorf("parse settings %s: %w", path, err)
	}
	s.Quality = normalizeQuality(s.Quality)
	if s.Source != sttSourceUser {
		s.Source = sttSourceAuto
	}

	gpu := detectGPU()
	cores := runtime.NumCPU()
	fingerprint := hardwareFingerprint(gpu, cores)

	if s.Source == sttSourceAuto {
		if fingerprint != s.HardwareFingerprint {
			// Hardware changed under an auto default: re-derive and rewrite.
			s = detectSettings()
			if err := Save(path, s); err != nil {
				return STTSettings{}, err
			}
		}
		return s, nil
	}

	// Source == user: never change policy, only refresh display fields.
	if s.DetectedGPU != gpu || s.Cores != cores || s.HardwareFingerprint != fingerprint {
		s.DetectedGPU = gpu
		s.Cores = cores
		s.HardwareFingerprint = fingerprint
		// Best-effort: a stale display field is harmless, so a write failure
		// here must not block startup.
		_ = Save(path, s)
	}
	return s, nil
}

// Save writes settings.json atomically (temp file + rename) so a crash mid-write
// cannot truncate the persisted config.
func Save(path string, s STTSettings) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("settings path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir settings dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write settings temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename settings: %w", err)
	}
	return nil
}

// ChildEnv reconciles base (a copy of os.Environ()) so the stored STT config
// wins over whatever the image baked in. It strips inherited STT keys and
// re-appends only what the policy dictates:
//
//   - CASSINI_STT_QUALITY is always set from the stored tier.
//   - CASSINI_STT_DEVICE / CASSINI_STT_MODEL are stripped, then re-appended only
//     when the user set an explicit override, so otherwise the recorder's
//     auto-detect + tier resolution takes over (the deployed image pins
//     CASSINI_STT_MODEL=int8, which must NOT shadow the chosen tier).
//   - CASSINI_STT_NUM_THREADS is always stripped: thread count is a host concern
//     the recorder derives from the core count; a baked value would override it.
func (s STTSettings) ChildEnv(base []string) []string {
	// Always strip the STT keys, then re-append exactly what the policy
	// dictates. Stripping unconditionally (rather than only when there is no
	// override) keeps the result duplicate-free and makes the appended override
	// the single, authoritative value.
	drop := map[string]bool{
		envSTTQuality:    true,
		envSTTNumThreads: true,
		envSTTDevice:     true,
		envSTTModel:      true,
	}

	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if drop[key] {
			continue
		}
		out = append(out, kv)
	}

	out = append(out, envSTTQuality+"="+normalizeQuality(s.Quality))
	if s.DeviceOverride != "" {
		out = append(out, envSTTDevice+"="+s.DeviceOverride)
	}
	if s.ModelOverride != "" {
		out = append(out, envSTTModel+"="+s.ModelOverride)
	}
	return out
}

// effectiveSTT is the resolved, human-readable view returned alongside the raw
// settings on GET: the recorder derives the actual model from quality+device,
// so the operator only reports the inputs and where they came from.
type effectiveSTT struct {
	Quality string `json:"quality"`
	// Device is the override when set, else "auto" — the recorder auto-detects.
	Device string `json:"device"`
	// Model is the override when set, else "" — the recorder derives it from
	// quality+device, so the operator does not predict it.
	Model string `json:"model,omitempty"`
	Note  string `json:"note"`
}

func (s STTSettings) effective() effectiveSTT {
	device := s.DeviceOverride
	if device == "" {
		device = "auto"
	}
	return effectiveSTT{
		Quality: normalizeQuality(s.Quality),
		Device:  device,
		Model:   s.ModelOverride,
		Note:    "the recorder auto-detects the device and derives the model from quality+device unless an explicit override is set",
	}
}

type settingsResponse struct {
	STTSettings
	Effective effectiveSTT `json:"effective"`
}

// settingsUpdate is the PUT body. Pointers distinguish "field omitted" from
// "field set to empty"; quality is required.
type settingsUpdate struct {
	Quality        string  `json:"quality"`
	DeviceOverride *string `json:"device_override"`
	ModelOverride  *string `json:"model_override"`
}

// currentSettings returns a copy of the in-memory STT policy, safe for
// concurrent reads at job-spawn time against PUT /settings writes (D-435).
func (rt *Runtime) currentSettings() STTSettings {
	rt.settingsMu.RLock()
	defer rt.settingsMu.RUnlock()
	return rt.settings
}

// setSettings replaces the in-memory STT policy under the write lock.
func (rt *Runtime) setSettings(s STTSettings) {
	rt.settingsMu.Lock()
	rt.settings = s
	rt.settingsMu.Unlock()
}

// validDeviceOverride reports whether a device override is one the recorder
// understands. "" and "auto" both mean "let the recorder auto-detect".
func validDeviceOverride(device string) bool {
	switch device {
	case "", "auto", "cpu", "cuda":
		return true
	default:
		return false
	}
}

func (rt *Runtime) settingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/settings" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rt.handleGetSettings(w)
	case http.MethodPut:
		rt.handlePutSettings(w, r)
	default:
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
	}
}

func (rt *Runtime) handleGetSettings(w http.ResponseWriter) {
	// Re-read from disk so an out-of-band edit (or a peer process) is reflected,
	// and refresh the in-memory copy used at job spawn time.
	s, err := LoadOrInitSettings(rt.settingsPath)
	if err != nil {
		// Disk unreadable: fall back to the in-memory copy rather than 500, so
		// the admin can still see and re-set the policy.
		s = rt.currentSettings()
	} else {
		rt.setSettings(s)
	}
	writeJSON(w, http.StatusOK, settingsResponse{STTSettings: s, Effective: s.effective()})
}

func (rt *Runtime) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	var in settingsUpdate
	if err := json.Unmarshal(raw, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}

	quality := strings.ToLower(strings.TrimSpace(in.Quality))
	switch quality {
	case sttQualityFast, sttQualityBalanced, sttQualityBest:
	default:
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("quality must be one of fast, balanced, best (got %q)", in.Quality))
		return
	}

	// Start from the current settings so unspecified override fields are
	// preserved; the host display fields are refreshed below.
	updated := rt.currentSettings()
	updated.Quality = quality
	updated.Source = sttSourceUser
	if in.DeviceOverride != nil {
		device := strings.ToLower(strings.TrimSpace(*in.DeviceOverride))
		if !validDeviceOverride(device) {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("device_override must be one of \"\", auto, cpu, cuda (got %q)", *in.DeviceOverride))
			return
		}
		if device == "auto" {
			device = ""
		}
		updated.DeviceOverride = device
	}
	if in.ModelOverride != nil {
		updated.ModelOverride = strings.TrimSpace(*in.ModelOverride)
	}

	// Refresh the host display fields so the persisted record reflects the
	// current hardware even as the user pins policy.
	gpu := detectGPU()
	cores := runtime.NumCPU()
	updated.DetectedGPU = gpu
	updated.Cores = cores
	updated.HardwareFingerprint = hardwareFingerprint(gpu, cores)

	if err := Save(rt.settingsPath, updated); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save settings: %v", err))
		return
	}
	rt.setSettings(updated)
	writeJSON(w, http.StatusOK, settingsResponse{STTSettings: updated, Effective: updated.effective()})
}
