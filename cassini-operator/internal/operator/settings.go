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
	"unicode/utf8"
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
	Quality             string   `json:"quality"`
	DeviceOverride      string   `json:"device_override,omitempty"`
	ModelOverride       string   `json:"model_override,omitempty"`
	TranscriptionTerms  []string `json:"transcription_terms,omitempty"`
	Source              string   `json:"source"` // "auto" | "user"
	HardwareFingerprint string   `json:"hardware_fingerprint"`
	DetectedGPU         bool     `json:"detected_gpu"`
	Cores               int      `json:"cores"`
}

const (
	sttQualityFast     = "fast"
	sttQualityBalanced = "balanced"
	sttQualityBest     = "best"

	sttSourceAuto = "auto"
	sttSourceUser = "user"

	envSTTQuality           = "CASSINI_STT_QUALITY"
	envSTTNumThreads        = "CASSINI_STT_NUM_THREADS"
	envSTTStreamConcurrency = "CASSINI_STT_STREAM_CONCURRENCY"
	envSTTAdditionalModels  = "CASSINI_STT_ADDITIONAL_MODELS"
	envTranscriptionTerms   = "CASSINI_TRANSCRIPTION_TERMS"

	// auditedCUDAParakeetV3 is currently the only model whose complete
	// production path has been measured with the bundled CUDA runtime. Add a
	// model to validCUDAModelOverride only after an equivalent GPU/CPU-fallback
	// audit; accepting arbitrary model IDs would undermine GPU-only admission.
	auditedCUDAParakeetV3 = "parakeet-tdt-0.6b-v3"

	maxTranscriptionTerms     = 100
	maxTranscriptionTermRunes = 100
)

// SettingsMigration describes a persisted STT policy that was accepted by an
// older operator but cannot be executed under the current GPU-only policy. The
// loader clears only the unsupported overrides and rewrites settings.json; the
// remaining user policy stays intact.
//
// Callers receive this value only after the healed file has been persisted, so
// logging Message cannot claim a migration that failed to reach disk.
type SettingsMigration struct {
	Path                   string
	ClearedDeviceOverride  string
	ClearedModelOverride   string
	Quality                string
	Source                 string
	TranscriptionTermCount int
}

// Message is suitable for the operator log. It explains both what changed and
// how an administrator can restore an explicit, supported override without
// logging the vocabulary itself.
func (m SettingsMigration) Message() string {
	cleared := make([]string, 0, 2)
	if m.ClearedDeviceOverride != "" {
		cleared = append(cleared, fmt.Sprintf("unsupported device_override=%q", m.ClearedDeviceOverride))
	}
	if m.ClearedModelOverride != "" {
		cleared = append(cleared, fmt.Sprintf("unaudited model_override=%q", m.ClearedModelOverride))
	}
	return fmt.Sprintf(
		"stt_settings migrated %s: cleared %s; preserved quality=%q, source=%q, and %d transcription terms; use Settings to select CUDA or the audited model %q if an explicit override is required",
		m.Path,
		strings.Join(cleared, " and "),
		m.Quality,
		m.Source,
		m.TranscriptionTermCount,
		auditedCUDAParakeetV3,
	)
}

// SettingsMigrationReporter is invoked after a legacy settings file has been
// healed and successfully replaced. Keeping reporting as an injected callback lets
// startup use the Runtime logger without coupling this persistence helper to a
// process-global logger.
type SettingsMigrationReporter func(SettingsMigration)

func validCUDAModelOverride(model string) bool {
	switch strings.TrimSpace(model) {
	case "", auditedCUDAParakeetV3:
		return true
	default:
		return false
	}
}

// normalizeTranscriptionTerms turns user-entered glossary rows into bounded
// preferred spellings for readable-transcript cleanup. The first spelling wins
// when entries differ only by case.
func normalizeTranscriptionTerms(terms []string) ([]string, error) {
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = strings.Join(strings.Fields(term), " ")
		if term == "" {
			continue
		}
		if utf8.RuneCountInString(term) > maxTranscriptionTermRunes {
			return nil, fmt.Errorf("term %q exceeds %d characters", term, maxTranscriptionTermRunes)
		}
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			continue
		}
		if len(out) == maxTranscriptionTerms {
			return nil, fmt.Errorf("at most %d terms are allowed", maxTranscriptionTerms)
		}
		seen[key] = struct{}{}
		out = append(out, term)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

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
	return LoadOrInitSettingsWithMigrationReporter(path, nil)
}

// LoadOrInitSettingsWithMigrationReporter is LoadOrInitSettings with an
// optional structured notification for persisted-policy migrations.
func LoadOrInitSettingsWithMigrationReporter(path string, report SettingsMigrationReporter) (STTSettings, error) {
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
	s.TranscriptionTerms, err = normalizeTranscriptionTerms(s.TranscriptionTerms)
	if err != nil {
		return STTSettings{}, fmt.Errorf("parse settings %s transcription_terms: %w", path, err)
	}

	var migration SettingsMigration
	needsRewrite := false
	originalDeviceOverride := s.DeviceOverride
	deviceOverride := strings.ToLower(strings.TrimSpace(originalDeviceOverride))
	if deviceOverride == "auto" {
		deviceOverride = ""
	}
	if !validDeviceOverride(deviceOverride) {
		migration.ClearedDeviceOverride = originalDeviceOverride
		deviceOverride = ""
	}
	if deviceOverride != originalDeviceOverride {
		needsRewrite = true
	}
	s.DeviceOverride = deviceOverride

	originalModelOverride := s.ModelOverride
	s.ModelOverride = strings.TrimSpace(s.ModelOverride)
	if !validCUDAModelOverride(s.ModelOverride) {
		migration.ClearedModelOverride = originalModelOverride
		s.ModelOverride = ""
	}
	if s.ModelOverride != originalModelOverride {
		needsRewrite = true
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
			terms := s.TranscriptionTerms
			s = detectSettings()
			// Vocabulary is independent of the hardware-derived quality tier.
			// Preserve it when re-fingerprinting an auto policy.
			s.TranscriptionTerms = terms
			if err := Save(path, s); err != nil {
				return STTSettings{}, err
			}
			if migration.ClearedDeviceOverride != "" || migration.ClearedModelOverride != "" {
				reportSettingsMigration(report, path, migration, s)
			}
		} else if needsRewrite {
			if err := Save(path, s); err != nil {
				return STTSettings{}, err
			}
			if migration.ClearedDeviceOverride != "" || migration.ClearedModelOverride != "" {
				reportSettingsMigration(report, path, migration, s)
			}
		}
		return s, nil
	}

	// Source == user: never change policy, only refresh display fields.
	displayChanged := s.DetectedGPU != gpu || s.Cores != cores || s.HardwareFingerprint != fingerprint
	if displayChanged {
		s.DetectedGPU = gpu
		s.Cores = cores
		s.HardwareFingerprint = fingerprint
	}
	if needsRewrite {
		// Clearing a no-longer-supported override is a required migration, not
		// the best-effort display refresh below. Surface a write failure so the
		// operator cannot claim a self-heal that will recur on every restart.
		if err := Save(path, s); err != nil {
			return STTSettings{}, err
		}
		if migration.ClearedDeviceOverride != "" || migration.ClearedModelOverride != "" {
			reportSettingsMigration(report, path, migration, s)
		}
	} else if displayChanged {
		// Best-effort: a stale display field is harmless, so a write failure
		// here must not block startup.
		_ = Save(path, s)
	}
	return s, nil
}

func reportSettingsMigration(report SettingsMigrationReporter, path string, migration SettingsMigration, s STTSettings) {
	if report == nil {
		return
	}
	migration.Path = path
	migration.Quality = s.Quality
	migration.Source = s.Source
	migration.TranscriptionTermCount = len(s.TranscriptionTerms)
	report(migration)
}

// Save writes settings.json atomically (temp file + rename) so a crash mid-write
// cannot truncate the persisted config.
func Save(path string, s STTSettings) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("settings path must not be empty")
	}
	s.ModelOverride = strings.TrimSpace(s.ModelOverride)
	if !validCUDAModelOverride(s.ModelOverride) {
		return fmt.Errorf("model_override %q is not an audited CUDA model", s.ModelOverride)
	}
	deviceOverride := strings.ToLower(strings.TrimSpace(s.DeviceOverride))
	if !validDeviceOverride(deviceOverride) {
		return fmt.Errorf("device_override %q is not supported; operator builds are GPU-only", s.DeviceOverride)
	}
	if deviceOverride == "auto" {
		deviceOverride = ""
	}
	s.DeviceOverride = deviceOverride
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir settings dir: %w", err)
	}
	terms, err := normalizeTranscriptionTerms(s.TranscriptionTerms)
	if err != nil {
		return fmt.Errorf("normalize transcription_terms: %w", err)
	}
	s.TranscriptionTerms = terms
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
//   - CASSINI_STT_ADDITIONAL_MODELS is always stripped. Unreviewed secondary
//     graphs could fall back to CPU or allocate another model outside the
//     operator's GPU/RAM admission budget.
//   - CASSINI_TRANSCRIPTION_TERMS carries the optional, normalized preferred
//     spellings used only by LLM readable cleanup.
func (s STTSettings) ChildEnv(base []string) []string {
	// Always strip the STT keys, then re-append exactly what the policy
	// dictates. Stripping unconditionally (rather than only when there is no
	// override) keeps the result duplicate-free and makes the appended override
	// the single, authoritative value.
	drop := map[string]bool{
		envSTTQuality:          true,
		envSTTNumThreads:       true,
		envSTTDevice:           true,
		envSTTModel:            true,
		envSTTAdditionalModels: true,
		envTranscriptionTerms:  true,
	}

	out := make([]string, 0, len(base)+4)
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
	modelOverride := strings.TrimSpace(s.ModelOverride)
	if modelOverride != "" && validCUDAModelOverride(modelOverride) {
		out = append(out, envSTTModel+"="+modelOverride)
	}
	if terms, err := normalizeTranscriptionTerms(s.TranscriptionTerms); err == nil && len(terms) > 0 {
		encoded, _ := json.Marshal(terms)
		out = append(out, envTranscriptionTerms+"="+string(encoded))
	}
	return out
}

// effectiveSTT is the resolved, human-readable execution view returned
// alongside the raw settings on GET. It describes what an admitted operator
// build will actually receive after the GPU-only governor applies its policy.
type effectiveSTT struct {
	Quality string `json:"quality"`
	// Device is the concrete device the operator's resource governor injects
	// into every admitted build. Raw user intent remains available separately
	// as DeviceOverride.
	Device string `json:"device"`
	// Model is the concrete CUDA model selected by the recorder after the
	// governor has forced Device. Raw user intent remains available separately
	// as ModelOverride.
	Model string `json:"model,omitempty"`
	Note  string `json:"note"`
}

func (s STTSettings) effective() effectiveSTT {
	model := strings.TrimSpace(s.ModelOverride)
	if model == "" {
		// Every quality tier resolves to the fp32 v3 model on CUDA. Keep this
		// in step with the recorder's ModelForQuality policy and the resource
		// governor's forced-CUDA admission rule.
		model = auditedCUDAParakeetV3
	}
	return effectiveSTT{
		Quality: normalizeQuality(s.Quality),
		Device:  "cuda",
		Model:   model,
		Note:    "operator builds are GPU-only; admitted builds are forced to CUDA with one recognizer and one host thread",
	}
}

type settingsResponse struct {
	STTSettings
	Effective effectiveSTT `json:"effective"`
}

// settingsUpdate is the PUT body. Pointers distinguish "field omitted" from
// "field set to empty"; quality is required.
type settingsUpdate struct {
	Quality            string    `json:"quality"`
	DeviceOverride     *string   `json:"device_override"`
	ModelOverride      *string   `json:"model_override"`
	TranscriptionTerms *[]string `json:"transcription_terms"`
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

func (rt *Runtime) reportSettingsMigration(migration SettingsMigration) {
	if rt.logger != nil {
		rt.logger.Print(migration.Message())
	}
}

// validDeviceOverride reports whether a device override is permitted by the
// production operator. Raw recorder tooling can still support CPU explicitly,
// but operator-managed speech recognition is GPU-only.
func validDeviceOverride(device string) bool {
	switch device {
	case "", "auto", "cuda":
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
	s, err := LoadOrInitSettingsWithMigrationReporter(rt.settingsPath, rt.reportSettingsMigration)
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
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("device_override must be one of \"\", auto, cuda; operator builds are GPU-only (got %q)", *in.DeviceOverride))
			return
		}
		if device == "auto" {
			device = ""
		}
		updated.DeviceOverride = device
	}
	if in.ModelOverride != nil {
		model := strings.TrimSpace(*in.ModelOverride)
		if !validCUDAModelOverride(model) {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("model_override must be empty or the audited CUDA model %q (got %q)", auditedCUDAParakeetV3, *in.ModelOverride))
			return
		}
		updated.ModelOverride = model
	}
	if in.TranscriptionTerms != nil {
		terms, err := normalizeTranscriptionTerms(*in.TranscriptionTerms)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid transcription_terms: %v", err))
			return
		}
		updated.TranscriptionTerms = terms
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
