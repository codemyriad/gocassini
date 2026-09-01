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

	maxTranscriptionTerms     = 100
	maxTranscriptionTermRunes = 100
)

// SettingsMigration describes a persisted STT policy that names a device or
// model this operator cannot execute. The loader clears only the unsupported
// overrides and rewrites settings.json; the remaining user policy stays intact.
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
		"stt_settings migrated %s: cleared %s; preserved quality=%q, source=%q, and %d transcription terms; the policy now auto-selects the device, and Settings can still pin cpu or cuda, or one of the audited models %s",
		m.Path,
		strings.Join(cleared, " and "),
		m.Quality,
		m.Source,
		m.TranscriptionTermCount,
		strings.Join(auditedModels(), ", "),
	)
}

// SettingsMigrationReporter is invoked after a legacy settings file has been
// healed and successfully replaced. Keeping reporting as an injected callback lets
// startup use the Runtime logger without coupling this persistence helper to a
// process-global logger.
type SettingsMigrationReporter func(SettingsMigration)

// auditedModels are the models whose complete production path has been
// measured on the device that selects them. An explicit model_override is
// restricted to this list: accepting arbitrary IDs would let a build allocate
// an unmeasured graph outside the governor's RAM/VRAM budget.
func auditedModels() []string {
	return []string{modelParakeetV3Fp32, modelParakeetV3Int8, modelParakeet110M}
}

func validModelOverride(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	for _, audited := range auditedModels() {
		if model == audited {
			return true
		}
	}
	return false
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
	// CUDA capability is part of the fingerprint because it changes what a
	// build does without any hardware changing: swapping the plain image for
	// the -cuda one on the same host must re-derive an auto policy.
	return fmt.Sprintf("gpu=%t;cuda=%t;cores=%d", gpu, cudaCapableHost(), cores)
}

// defaultQualityForHardware picks the auto-default tier from the device builds
// will really use. On CUDA fp32 is both the quality ceiling and the fast
// option, so "best"; otherwise "balanced" (int8), the tier a 4-core host can
// sustain and whose RAM floor it can meet.
//
// This takes effective CUDA capability, not raw device visibility: the plain
// image installed on a GPU daemon sees /dev/nvidia* but transcribes on the CPU,
// and defaulting it to "best" would pick the 4.5GiB fp32 tier for a host that
// wanted balanced.
func defaultQualityForHardware(cudaCapable bool) string {
	if cudaCapable {
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
		Quality:             defaultQualityForHardware(cudaCapableHost()),
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
	if !validModelOverride(s.ModelOverride) {
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
	if !validModelOverride(s.ModelOverride) {
		return fmt.Errorf("model_override %q is not an audited model", s.ModelOverride)
	}
	deviceOverride := strings.ToLower(strings.TrimSpace(s.DeviceOverride))
	if !validDeviceOverride(deviceOverride) {
		return fmt.Errorf("device_override %q is not supported; expected \"\", auto, cpu or cuda", s.DeviceOverride)
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
	if modelOverride != "" && validModelOverride(modelOverride) {
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
// build will actually receive once the resource governor has applied its
// policy — the answer to "what will happen if I record right now", which the
// raw tier alone does not give on a host whose device is auto-selected.
type effectiveSTT struct {
	Quality string `json:"quality"`
	// Device is the concrete device the operator's resource governor will
	// inject into the next admitted build. Raw user intent remains available
	// separately as DeviceOverride.
	Device string `json:"device"`
	// Model is the concrete model the recorder will load on Device. Raw user
	// intent remains available separately as ModelOverride.
	Model string `json:"model,omitempty"`
	Note  string `json:"note"`
}

const (
	noteCUDA = "admitted builds run on CUDA with one recognizer and one host thread"
	noteCPU  = "no usable GPU on this host: builds run on the CPU, which is correct but much slower — " +
		"install the matching -cuda image on a GPU deploy daemon for GPU speed"
	notePinnedCPU    = "device_override=cpu: builds run on the CPU even if a GPU becomes available"
	noteCUDAUnusable = "device_override=cuda but this host has no usable CUDA runtime or device: builds stay " +
		"blocked until one is available, or until the override is cleared so they can fall back to the CPU"
)

func (s STTSettings) effective() effectiveSTT {
	device, note := effectiveDevice(s.DeviceOverride)
	model := s.modelForDevice(device)
	if !modelSupportsDevice(model, device) {
		note = fmt.Sprintf(
			"model_override %q is a CPU model and cannot run on CUDA: builds stay blocked until the model override is cleared or set to %s",
			model, modelParakeetV3Fp32)
	}
	return effectiveSTT{
		Quality: normalizeQuality(s.Quality),
		Device:  device,
		Model:   model,
		Note:    note,
	}
}

// effectiveDevice mirrors Runtime.resolveBuildDevice for display: it answers
// "which device will the next admitted build use", with one sentence an
// administrator can act on. It never fails — an unsatisfiable explicit override
// is reported as the device that was asked for, and /status carries the reason
// it is not usable.
func effectiveDevice(override string) (string, string) {
	cudaUsable := func() bool {
		capable, _ := imageCUDACapability()
		return capable && probeNVIDIADevice()
	}
	switch strings.ToLower(strings.TrimSpace(override)) {
	case deviceCPU:
		return deviceCPU, notePinnedCPU
	case deviceCUDA:
		if cudaUsable() {
			return deviceCUDA, noteCUDA
		}
		return deviceCUDA, noteCUDAUnusable
	default: // "", "auto", anything the loader has already normalised away
		if cudaUsable() {
			return deviceCUDA, noteCUDA
		}
		return deviceCPU, noteCPU
	}
}

// modelForDevice is the model an admitted build will load on device: the
// administrator's explicit override when there is one, else the quality tier's
// model for that device.
func (s STTSettings) modelForDevice(device string) string {
	if model := strings.TrimSpace(s.ModelOverride); model != "" {
		return model
	}
	return modelForQuality(s.Quality, device)
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

// validDeviceOverride reports whether a device override is one the operator can
// execute. Empty/auto lets the governor choose; cpu and cuda pin the choice.
func validDeviceOverride(device string) bool {
	switch device {
	case "", "auto", deviceCPU, deviceCUDA:
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
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("device_override must be one of \"\", auto, cpu, cuda (got %q)", *in.DeviceOverride))
			return
		}
		if device == "auto" {
			device = ""
		}
		updated.DeviceOverride = device
	}
	if in.ModelOverride != nil {
		model := strings.TrimSpace(*in.ModelOverride)
		if !validModelOverride(model) {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("model_override must be empty or one of the audited models %s (got %q)", strings.Join(auditedModels(), ", "), *in.ModelOverride))
			return
		}
		updated.ModelOverride = model
	}
	// Reject a pair that could never run rather than saving it and failing at
	// build time: an int8 model under CUDA fragments back onto the host CPU, so
	// it is neither the device nor the model the administrator asked for.
	if updated.ModelOverride != "" && strings.EqualFold(updated.DeviceOverride, deviceCUDA) &&
		!modelSupportsDevice(updated.ModelOverride, deviceCUDA) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf(
			"model_override %q is a CPU model and cannot be combined with device_override=cuda; select %s or clear the device override",
			updated.ModelOverride, modelParakeetV3Fp32))
		return
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
