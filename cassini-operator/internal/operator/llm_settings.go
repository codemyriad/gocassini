package operator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// LLMSettings is the operator-owned, persisted LLM policy: the endpoints an
// administrator has registered and which one the meeting summary runs on. It lives beside
// settings.json on the AppAPI volume and is the single source of the
// recorder's LLM environment: ChildEnv strips every inherited LLM variable and
// re-emits exactly what the policy says, so changing endpoints never needs an
// ExApp redeploy (D-696).
//
// The deploy environment (LLM_BASE_URL, OPENROUTER_API_KEY, LLM_MODEL and the
// recorder's kill-switches) seeds the file once, on first start, and is
// ignored after that. API keys are persisted here and never served.
type LLMSettings struct {
	Providers []LLMProvider `json:"providers"`
	Summary   LLMStep       `json:"summary"`
}

// LLMProvider is one OpenAI-compatible chat-completions endpoint.
type LLMProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	// APIKey is write-only from the API's point of view: GET reports
	// api_key_configured instead of the value.
	APIKey string `json:"api_key,omitempty"`
	// TimeoutSec and MaxTokens bound each request to this endpoint; 0 means
	// the recorder default (900s / 4096 tokens). Per endpoint, because they
	// describe the host: a CPU-bound local model needs a longer leash than a
	// hosted API.
	TimeoutSec int `json:"timeout_sec,omitempty"`
	MaxTokens  int `json:"max_tokens,omitempty"`
}

// LLMStep is the policy for one LLM step: whether it runs, on which provider,
// with which model. An empty model leaves the recorder's default in place.
type LLMStep struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

const (
	envLLMAPIKey        = "OPENROUTER_API_KEY"
	envLLMBaseURLLegacy = "OPENROUTER_BASE_URL"
	envLLMBaseURL       = "LLM_BASE_URL"
	envLLMModel         = "LLM_MODEL"
	envSummaryModel     = "SUMMARY_MODEL"
	envSummaryDisabled  = "CASSINI_SUMMARY_DISABLED"
	envReadableDisabled = "CASSINI_READABLE_DISABLED"
	envLLMTimeoutSec    = "CASSINI_LLM_TIMEOUT_SEC"
	envLLMMaxTokens     = "CASSINI_LLM_MAX_TOKENS"

	llmStepSummary = "SUMMARY"

	openRouterBaseURL = "https://openrouter.ai/api/v1"

	maxLLMProviders      = 20
	maxLLMFieldRunes     = 200
	llmDiscoveryTimeout  = 20 * time.Second
	llmDiscoveryMaxBytes = 8 << 20
)

var llmProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// llmStepEnv names the per-step variables the recorder reads for one step.
func llmStepEnv(step string) (baseURL, apiKey, model string) {
	return step + "_BASE_URL", step + "_API_KEY", step + "_MODEL"
}

// inheritedLLMEnv is every LLM variable ChildEnv strips from the inherited
// environment before re-emitting the policy: the shared trio and switches the
// recorder reads when run by hand, and the per-step set the operator emits.
func inheritedLLMEnv() map[string]bool {
	drop := map[string]bool{
		envLLMAPIKey: true, envLLMBaseURLLegacy: true, envLLMBaseURL: true, envLLMModel: true,
		envSummaryModel: true, envSummaryDisabled: true, envLLMTimeoutSec: true, envLLMMaxTokens: true,
		// Retired names an older deploy may still carry: the readable-cleanup
		// kill switch and its per-step wire (the cleanup step was removed).
		envReadableDisabled: true, "READABLE_BASE_URL": true, "READABLE_API_KEY": true, "READABLE_MODEL": true,
	}
	b, k, m := llmStepEnv(llmStepSummary)
	drop[b], drop[k], drop[m] = true, true, true
	return drop
}

func llmSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), "llm-settings.json")
}

func envBoolFrom(getenv func(string) string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envIntFrom(getenv func(string) string, key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv(key)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// llmProviderNameFor derives a display name from an endpoint URL.
func llmProviderNameFor(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "Default"
	}
	if host := strings.ToLower(u.Hostname()); host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai") {
		return "OpenRouter"
	}
	return u.Host
}

// SeedLLMSettings derives the first-start policy from the deploy environment,
// mirroring what the recorder would have read by hand: the shared endpoint
// becomes the one provider and both steps run on it unless their kill-switch
// is set. With no endpoint the policy is empty and both steps are off.
func SeedLLMSettings(getenv func(string) string) LLMSettings {
	s := LLMSettings{Providers: []LLMProvider{}}
	key := strings.TrimSpace(getenv(envLLMAPIKey))
	base := strings.TrimSpace(getenv(envLLMBaseURLLegacy))
	if base == "" {
		base = strings.TrimSpace(getenv(envLLMBaseURL))
	}
	if base == "" && key != "" {
		base = openRouterBaseURL
	}
	if base == "" {
		return s
	}
	provider := LLMProvider{
		ID: "default", Name: llmProviderNameFor(base), BaseURL: base, APIKey: key,
		TimeoutSec: envIntFrom(getenv, envLLMTimeoutSec),
		MaxTokens:  envIntFrom(getenv, envLLMMaxTokens),
	}
	s.Providers = append(s.Providers, provider)
	summaryModel := strings.TrimSpace(getenv(envSummaryModel))
	if summaryModel == "" {
		summaryModel = strings.TrimSpace(getenv(envLLMModel))
	}
	s.Summary = LLMStep{Enabled: !envBoolFrom(getenv, envSummaryDisabled), Provider: provider.ID, Model: summaryModel}
	return s
}

// LoadOrInitLLMSettings loads llm-settings.json, or — on first start — seeds
// it from the deploy environment and writes it.
func LoadOrInitLLMSettings(path string, getenv func(string) string) (LLMSettings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s := SeedLLMSettings(getenv)
			if err := SaveLLMSettings(path, s); err != nil {
				return LLMSettings{}, err
			}
			return s, nil
		}
		return LLMSettings{}, fmt.Errorf("read llm settings: %w", err)
	}
	var s LLMSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return LLMSettings{}, fmt.Errorf("parse llm settings %s: %w", path, err)
	}
	s, err = normalizeLLMSettings(s)
	if err != nil {
		return LLMSettings{}, fmt.Errorf("invalid llm settings %s: %w", path, err)
	}
	return s, nil
}

// SaveLLMSettings writes llm-settings.json atomically. The file holds API
// keys, so it is owner-only.
func SaveLLMSettings(path string, s LLMSettings) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("llm settings path must not be empty")
	}
	s, err := normalizeLLMSettings(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir settings dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal llm settings: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write llm settings temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename llm settings: %w", err)
	}
	return nil
}

// normalizeLLMSettings trims and validates a policy. It is the one gate for
// both the API and the file, so a persisted policy always resolves.
func normalizeLLMSettings(s LLMSettings) (LLMSettings, error) {
	if len(s.Providers) > maxLLMProviders {
		return s, fmt.Errorf("at most %d providers are allowed", maxLLMProviders)
	}
	providers := make([]LLMProvider, 0, len(s.Providers))
	ids := make(map[string]struct{}, len(s.Providers))
	for _, p := range s.Providers {
		p.ID = strings.TrimSpace(p.ID)
		p.Name = strings.Join(strings.Fields(p.Name), " ")
		p.BaseURL = strings.TrimSpace(p.BaseURL)
		p.APIKey = strings.TrimSpace(p.APIKey)
		if !llmProviderIDPattern.MatchString(p.ID) {
			return s, fmt.Errorf("provider id %q must be letters, digits, '.', '_' or '-'", p.ID)
		}
		if _, dup := ids[p.ID]; dup {
			return s, fmt.Errorf("duplicate provider id %q", p.ID)
		}
		ids[p.ID] = struct{}{}
		if err := validLLMBaseURL(p.BaseURL); err != nil {
			return s, fmt.Errorf("provider %q: %w", p.ID, err)
		}
		if p.Name == "" {
			p.Name = llmProviderNameFor(p.BaseURL)
		}
		if p.TimeoutSec < 0 {
			return s, fmt.Errorf("provider %q: timeout_sec must not be negative", p.ID)
		}
		if p.MaxTokens < 0 {
			return s, fmt.Errorf("provider %q: max_tokens must not be negative", p.ID)
		}
		for _, f := range []struct{ name, value string }{{"id", p.ID}, {"name", p.Name}, {"base_url", p.BaseURL}} {
			if utf8.RuneCountInString(f.value) > maxLLMFieldRunes {
				return s, fmt.Errorf("provider %q: %s exceeds %d characters", p.ID, f.name, maxLLMFieldRunes)
			}
		}
		providers = append(providers, p)
	}
	s.Providers = providers
	var err error
	if s.Summary, err = normalizeLLMStep("summary", s.Summary, ids); err != nil {
		return s, err
	}
	return s, nil
}

func normalizeLLMStep(name string, step LLMStep, providers map[string]struct{}) (LLMStep, error) {
	step.Provider = strings.TrimSpace(step.Provider)
	step.Model = strings.TrimSpace(step.Model)
	if utf8.RuneCountInString(step.Model) > maxLLMFieldRunes {
		return step, fmt.Errorf("%s: model exceeds %d characters", name, maxLLMFieldRunes)
	}
	if _, ok := providers[step.Provider]; !ok {
		if step.Enabled {
			if step.Provider == "" {
				return step, fmt.Errorf("%s is enabled but has no provider", name)
			}
			return step, fmt.Errorf("%s refers to unknown provider %q", name, step.Provider)
		}
		// A disabled step may outlive its provider; forget the reference.
		step.Provider = ""
	}
	return step, nil
}

func validLLMBaseURL(raw string) error {
	if raw == "" {
		return errors.New("base_url must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base_url %q must be an http(s) URL", raw)
	}
	return nil
}

func newLLMProviderID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "p-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "p-" + hex.EncodeToString(b[:])
}

// provider returns the provider a step resolves to, or false when the step is
// off or its provider is gone.
func (s LLMSettings) provider(step LLMStep) (LLMProvider, bool) {
	if !step.Enabled {
		return LLMProvider{}, false
	}
	for _, p := range s.Providers {
		if p.ID == step.Provider {
			return p, true
		}
	}
	return LLMProvider{}, false
}

// ChildEnv reconciles base (a copy of os.Environ()) so the persisted LLM
// policy wins over whatever the container was deployed with. Every inherited
// LLM variable is stripped — including the shared LLM_BASE_URL both steps
// would otherwise inherit — and each step gets its own fully resolved
// endpoint, or nothing at all when it is off. The recorder's kill-switches are
// never emitted: "off" is simply the absence of an endpoint.
func (s LLMSettings) ChildEnv(base []string) []string {
	drop := inheritedLLMEnv()
	out := make([]string, 0, len(base)+8)
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
	out = s.appendStepEnv(out, llmStepSummary, s.Summary)
	return out
}

func (s LLMSettings) appendStepEnv(out []string, name string, step LLMStep) []string {
	p, ok := s.provider(step)
	if !ok {
		return out
	}
	baseKey, keyKey, modelKey := llmStepEnv(name)
	out = append(out, baseKey+"="+p.BaseURL)
	if p.APIKey != "" {
		out = append(out, keyKey+"="+p.APIKey)
	}
	if step.Model != "" {
		out = append(out, modelKey+"="+step.Model)
	}
	if p.TimeoutSec > 0 {
		out = append(out, envLLMTimeoutSec+"="+strconv.Itoa(p.TimeoutSec))
	}
	if p.MaxTokens > 0 {
		out = append(out, envLLMMaxTokens+"="+strconv.Itoa(p.MaxTokens))
	}
	return out
}

// currentLLMSettings returns a copy of the in-memory LLM policy, safe for
// concurrent reads at job-spawn time against PUT /settings/llm writes.
func (rt *Runtime) currentLLMSettings() LLMSettings {
	rt.llmMu.RLock()
	defer rt.llmMu.RUnlock()
	s := rt.llm
	s.Providers = append([]LLMProvider(nil), rt.llm.Providers...)
	return s
}

func (rt *Runtime) setLLMSettings(s LLMSettings) {
	rt.llmMu.Lock()
	rt.llm = s
	rt.llmMu.Unlock()
}

// childEnv is the environment every recorder subprocess receives: the
// operator's own environment with the persisted STT and LLM policies applied
// on top.
func (rt *Runtime) childEnv() []string {
	return rt.currentLLMSettings().ChildEnv(rt.currentSettings().ChildEnv(os.Environ()))
}

// --- HTTP: GET/PUT /settings/llm, GET /settings/llm/providers/{id}/models ---

type llmProviderView struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	TimeoutSec       int    `json:"timeout_sec"`
	MaxTokens        int    `json:"max_tokens"`
}

// llmEffectiveStep is what the recorder will actually receive for a step; nil
// in the response means the step is off.
type llmEffectiveStep struct {
	Provider         string `json:"provider"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model,omitempty"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

type llmEffective struct {
	Summary *llmEffectiveStep `json:"summary"`
}

type llmSettingsResponse struct {
	Providers []llmProviderView `json:"providers"`
	Summary   LLMStep           `json:"summary"`
	Effective llmEffective      `json:"effective"`
}

func (s LLMSettings) view() llmSettingsResponse {
	providers := make([]llmProviderView, 0, len(s.Providers))
	for _, p := range s.Providers {
		providers = append(providers, llmProviderView{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, APIKeyConfigured: p.APIKey != "",
			TimeoutSec: p.TimeoutSec, MaxTokens: p.MaxTokens,
		})
	}
	return llmSettingsResponse{
		Providers: providers,
		Summary:   s.Summary,
		Effective: llmEffective{Summary: s.effectiveStep(s.Summary)},
	}
}

func (s LLMSettings) effectiveStep(step LLMStep) *llmEffectiveStep {
	p, ok := s.provider(step)
	if !ok {
		return nil
	}
	return &llmEffectiveStep{Provider: p.ID, BaseURL: p.BaseURL, Model: step.Model, APIKeyConfigured: p.APIKey != ""}
}

// llmProviderUpdate is one provider in a PUT body. APIKey distinguishes
// "omitted" (keep the stored key for this id) from "" (clear it).
type llmProviderUpdate struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	BaseURL    string  `json:"base_url"`
	APIKey     *string `json:"api_key"`
	TimeoutSec int     `json:"timeout_sec"`
	MaxTokens  int     `json:"max_tokens"`
}

// llmSettingsUpdate is the PUT body. Every field is optional; a present
// providers list replaces the stored one.
type llmSettingsUpdate struct {
	Providers *[]llmProviderUpdate `json:"providers"`
	Summary   *LLMStep             `json:"summary"`
}

func (rt *Runtime) llmSettingsHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/settings/llm":
		switch r.Method {
		case http.MethodGet:
			rt.handleGetLLMSettings(w)
		case http.MethodPut:
			rt.handlePutLLMSettings(w, r)
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
		}
	case strings.HasPrefix(path, "/settings/llm/providers/") && strings.HasSuffix(path, "/models"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/settings/llm/providers/"), "/models")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		rt.handleLLMProviderModels(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (rt *Runtime) handleGetLLMSettings(w http.ResponseWriter) {
	// Re-read from disk so an out-of-band edit is reflected, and refresh the
	// in-memory copy used at job spawn time; fall back to memory if the disk
	// is unreadable so the admin can still see and re-set the policy.
	s, err := LoadOrInitLLMSettings(rt.llmSettingsPath, os.Getenv)
	if err != nil {
		s = rt.currentLLMSettings()
	} else {
		rt.setLLMSettings(s)
	}
	writeJSON(w, http.StatusOK, s.view())
}

func (rt *Runtime) handlePutLLMSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	var in llmSettingsUpdate
	if err := json.Unmarshal(raw, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}

	updated := rt.currentLLMSettings()
	if in.Providers != nil {
		stored := make(map[string]LLMProvider, len(updated.Providers))
		for _, p := range updated.Providers {
			stored[p.ID] = p
		}
		providers := make([]LLMProvider, 0, len(*in.Providers))
		for _, p := range *in.Providers {
			id := strings.TrimSpace(p.ID)
			if id == "" {
				id = newLLMProviderID()
			}
			next := LLMProvider{ID: id, Name: p.Name, BaseURL: p.BaseURL, TimeoutSec: p.TimeoutSec, MaxTokens: p.MaxTokens}
			if p.APIKey != nil {
				next.APIKey = *p.APIKey
			} else if old, ok := stored[id]; ok {
				next.APIKey = old.APIKey
			}
			providers = append(providers, next)
		}
		updated.Providers = providers
	}
	if in.Summary != nil {
		updated.Summary = *in.Summary
	}
	updated, err = normalizeLLMSettings(updated)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := SaveLLMSettings(rt.llmSettingsPath, updated); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save llm settings: %v", err))
		return
	}
	rt.setLLMSettings(updated)
	writeJSON(w, http.StatusOK, updated.view())
}

type llmModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
}

type llmModelsResponse struct {
	Provider string     `json:"provider"`
	Models   []llmModel `json:"models"`
}

func (rt *Runtime) handleLLMProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	s := rt.currentLLMSettings()
	var provider *LLMProvider
	for i := range s.Providers {
		if s.Providers[i].ID == id {
			provider = &s.Providers[i]
			break
		}
	}
	if provider == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown provider %q", id))
		return
	}
	models, err := listLLMModels(r.Context(), *provider)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("list models from %s: %v", provider.Name, err))
		return
	}
	writeJSON(w, http.StatusOK, llmModelsResponse{Provider: id, Models: models})
}

// listLLMModels asks an OpenAI-compatible endpoint what it serves. Hosted
// providers and the self-hosted servers that matter (llama.cpp, vLLM, Ollama,
// LM Studio) all answer GET {base}/models with {"data":[{"id":...}]}; the key
// stays server-side and the endpoint only has to be reachable from here.
func listLLMModels(ctx context.Context, p LLMProvider) ([]llmModel, error) {
	ctx, cancel := context.WithTimeout(ctx, llmDiscoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, llmDiscoveryMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > llmDiscoveryMaxBytes {
		return nil, errors.New("response too large")
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return nil, errors.New("not an OpenAI-compatible model list")
	}
	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]llmModel, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, llmModel{ID: id, Name: strings.TrimSpace(m.Name), ContextLength: m.ContextLength})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
