package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func llmGetenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// clearLLMEnv keeps a developer's own LLM variables out of the seeded policy.
func clearLLMEnv(t *testing.T) {
	t.Helper()
	for key := range inheritedLLMEnv() {
		t.Setenv(key, "")
	}
}

func TestSeedLLMSettingsFromKeylessEndpoint(t *testing.T) {
	s := SeedLLMSettings(llmGetenv(map[string]string{
		envLLMBaseURL: "http://qwen.internal:8000/v1",
		envLLMModel:   "qwen3-30b",
	}))
	if len(s.Providers) != 1 {
		t.Fatalf("providers = %+v, want one", s.Providers)
	}
	p := s.Providers[0]
	if p.ID != "default" || p.Name != "qwen.internal:8000" || p.BaseURL != "http://qwen.internal:8000/v1" || p.APIKey != "" {
		t.Fatalf("provider = %+v", p)
	}
	if !s.Summary.Enabled || s.Summary.Provider != "default" || s.Summary.Model != "qwen3-30b" {
		t.Fatalf("summary = %+v, want enabled on default with qwen3-30b", s.Summary)
	}
}

func TestSeedLLMSettingsKeyAloneImpliesOpenRouter(t *testing.T) {
	s := SeedLLMSettings(llmGetenv(map[string]string{envLLMAPIKey: "sk-or-secret"}))
	if len(s.Providers) != 1 {
		t.Fatalf("providers = %+v, want one", s.Providers)
	}
	p := s.Providers[0]
	if p.BaseURL != openRouterBaseURL || p.Name != "OpenRouter" || p.APIKey != "sk-or-secret" {
		t.Fatalf("provider = %+v", p)
	}
}

func TestSeedLLMSettingsHonoursSwitchAndSummaryModel(t *testing.T) {
	s := SeedLLMSettings(llmGetenv(map[string]string{
		envLLMBaseURL:      "http://qwen.internal:8000/v1",
		envLLMModel:        "small",
		envSummaryModel:    "large",
		envSummaryDisabled: "1",
		envLLMTimeoutSec:   "1800",
		envLLMMaxTokens:    "8192",
	}))
	if s.Summary.Enabled || s.Summary.Provider != "default" {
		t.Fatalf("summary = %+v, want disabled but still pointing at default", s.Summary)
	}
	if s.Summary.Model != "large" {
		t.Fatalf("summary model = %q, want large", s.Summary.Model)
	}
	if s.TimeoutSec != 1800 || s.MaxTokens != 8192 {
		t.Fatalf("bounds = %d/%d", s.TimeoutSec, s.MaxTokens)
	}
}

func TestSeedLLMSettingsEmptyWithoutEndpoint(t *testing.T) {
	s := SeedLLMSettings(llmGetenv(map[string]string{envLLMModel: "ignored"}))
	if len(s.Providers) != 0 || s.Summary.Enabled {
		t.Fatalf("settings = %+v, want empty and off", s)
	}
	env := s.ChildEnv([]string{"PATH=/bin"})
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("ChildEnv = %v, want only PATH", env)
	}
}

func TestLLMChildEnvStripsInheritedAndEmitsPerStep(t *testing.T) {
	s := LLMSettings{
		Providers: []LLMProvider{
			{ID: "hosted", Name: "OpenRouter", BaseURL: openRouterBaseURL, APIKey: "sk-or-secret"},
			{ID: "local", Name: "Qwen", BaseURL: "http://qwen.internal:8000/v1"},
		},
		Summary:    LLMStep{Enabled: true, Provider: "hosted", Model: "big"},
		TimeoutSec: 600,
	}
	base := []string{
		"PATH=/bin",
		"OPENROUTER_API_KEY=deploy-key",
		"OPENROUTER_BASE_URL=https://stale.example/v1",
		"LLM_BASE_URL=https://stale.example/v1",
		"LLM_MODEL=stale",
		"SUMMARY_MODEL=stale",
		"CASSINI_SUMMARY_DISABLED=1",
		"CASSINI_READABLE_DISABLED=1",
		"READABLE_BASE_URL=https://stale.example/v1",
		"CASSINI_LLM_MAX_TOKENS=1",
	}
	env := s.ChildEnv(base)

	want := map[string]string{
		"PATH":                    "/bin",
		"SUMMARY_BASE_URL":        openRouterBaseURL,
		"SUMMARY_API_KEY":         "sk-or-secret",
		"SUMMARY_MODEL":           "big",
		"CASSINI_LLM_TIMEOUT_SEC": "600",
	}
	for key, value := range want {
		if got, ok := envValue(env, key); !ok || got != value {
			t.Fatalf("%s = %q (present=%v), want %q; env=%v", key, got, ok, value, env)
		}
	}
	for _, key := range []string{
		"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "LLM_BASE_URL", "LLM_MODEL",
		"CASSINI_SUMMARY_DISABLED", "CASSINI_READABLE_DISABLED", "CASSINI_LLM_MAX_TOKENS",
		"READABLE_BASE_URL", "READABLE_API_KEY", "READABLE_MODEL",
	} {
		if got, ok := envValue(env, key); ok {
			t.Fatalf("%s should be absent, got %q", key, got)
		}
	}
	if len(env) != len(want) {
		t.Fatalf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
}

func TestLLMChildEnvDisabledStepEmitsNothing(t *testing.T) {
	s := LLMSettings{
		Providers: []LLMProvider{{ID: "local", BaseURL: "http://qwen.internal:8000/v1"}},
		Summary:   LLMStep{Enabled: false, Provider: "local", Model: "cheap"},
	}
	env := s.ChildEnv(nil)
	if _, ok := envValue(env, "SUMMARY_BASE_URL"); ok {
		t.Fatalf("disabled summary step leaked into env: %v", env)
	}
}

func TestLoadOrInitLLMSettingsSeedsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm-settings.json")
	getenv := llmGetenv(map[string]string{envLLMAPIKey: "sk-or-secret", envLLMModel: "openai/gpt-4o-mini"})

	seeded, err := LoadOrInitLLMSettings(path, getenv)
	if err != nil {
		t.Fatalf("LoadOrInitLLMSettings() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("settings file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (file holds API keys)", info.Mode().Perm())
	}

	reloaded, err := LoadOrInitLLMSettings(path, llmGetenv(nil))
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if len(reloaded.Providers) != 1 || reloaded.Providers[0].APIKey != "sk-or-secret" {
		t.Fatalf("reloaded = %+v, want the persisted key", reloaded)
	}
	if reloaded.Summary != seeded.Summary {
		t.Fatalf("reloaded summary %+v differs from seeded %+v", reloaded.Summary, seeded.Summary)
	}
}

func TestNormalizeLLMSettingsRejectsUnresolvablePolicy(t *testing.T) {
	local := LLMProvider{ID: "local", BaseURL: "http://qwen.internal:8000/v1"}
	cases := map[string]LLMSettings{
		"enabled step without provider": {Providers: []LLMProvider{local}, Summary: LLMStep{Enabled: true}},
		"enabled step unknown provider": {Providers: []LLMProvider{local}, Summary: LLMStep{Enabled: true, Provider: "gone"}},
		"duplicate ids":                 {Providers: []LLMProvider{local, local}},
		"bad scheme":                    {Providers: []LLMProvider{{ID: "x", BaseURL: "ftp://qwen.internal/v1"}}},
		"not a url":                     {Providers: []LLMProvider{{ID: "x", BaseURL: "qwen"}}},
		"id with slash":                 {Providers: []LLMProvider{{ID: "a/b", BaseURL: "http://qwen.internal/v1"}}},
		"negative timeout":              {TimeoutSec: -1},
	}
	for name, in := range cases {
		if _, err := normalizeLLMSettings(in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNormalizeLLMSettingsForgetsDanglingDisabledProvider(t *testing.T) {
	s, err := normalizeLLMSettings(LLMSettings{Summary: LLMStep{Enabled: false, Provider: "gone", Model: "m"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Summary.Provider != "" || s.Summary.Model != "m" {
		t.Fatalf("summary = %+v, want provider cleared and model kept", s.Summary)
	}
}

func decodeLLMSettings(t *testing.T, rec *httptest.ResponseRecorder) llmSettingsResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out llmSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return out
}

func TestGetLLMSettingsRedactsKeys(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv(envLLMAPIKey, "sk-or-secret-value")
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm", nil))
	out := decodeLLMSettings(t, rec)

	if strings.Contains(rec.Body.String(), "sk-or-secret-value") || strings.Contains(rec.Body.String(), `"api_key"`) {
		t.Fatalf("GET leaked the key: %s", rec.Body.String())
	}
	if len(out.Providers) != 1 || !out.Providers[0].APIKeyConfigured || out.Providers[0].Name != "OpenRouter" {
		t.Fatalf("providers = %+v", out.Providers)
	}
	if out.Effective.Summary == nil || out.Effective.Summary.BaseURL != openRouterBaseURL || !out.Effective.Summary.APIKeyConfigured {
		t.Fatalf("effective summary = %+v", out.Effective.Summary)
	}
}

func TestPutLLMSettingsSwitchesEndpointWithoutTouchingStoredKey(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv(envLLMAPIKey, "sk-or-secret-value")
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	// The hosted provider is re-sent without api_key (keep it); a keyless local
	// one is added and the summary moves there — the prod cutover in one call.
	body := `{
	  "providers": [
	    {"id": "default", "name": "OpenRouter", "base_url": "https://openrouter.ai/api/v1"},
	    {"id": "local", "name": "Qwen", "base_url": "http://qwen.internal:8000/v1"}
	  ],
	  "summary": {"enabled": true, "provider": "local", "model": "qwen3-30b"}
	}`
	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(body)))
	out := decodeLLMSettings(t, rec)

	if len(out.Providers) != 2 || !out.Providers[0].APIKeyConfigured || out.Providers[1].APIKeyConfigured {
		t.Fatalf("providers = %+v", out.Providers)
	}
	if out.Effective.Summary == nil || out.Effective.Summary.BaseURL != "http://qwen.internal:8000/v1" || out.Effective.Summary.Model != "qwen3-30b" {
		t.Fatalf("effective summary = %+v", out.Effective.Summary)
	}

	stored, err := LoadOrInitLLMSettings(rt.llmSettingsPath, llmGetenv(nil))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Providers[0].APIKey != "sk-or-secret-value" {
		t.Fatalf("omitting api_key must keep the stored key, got %q", stored.Providers[0].APIKey)
	}
	env := rt.childEnv()
	if got, _ := envValue(env, "SUMMARY_BASE_URL"); got != "http://qwen.internal:8000/v1" {
		t.Fatalf("SUMMARY_BASE_URL = %q after PUT; env=%v", got, env)
	}
	if _, ok := envValue(env, "OPENROUTER_API_KEY"); ok {
		t.Fatalf("deploy key must not reach the recorder: %v", env)
	}
	if _, ok := envValue(env, "CASSINI_STT_QUALITY"); !ok {
		t.Fatalf("STT policy missing from the combined child env: %v", env)
	}

	// An explicit empty api_key clears it.
	rec = httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(
		`{"providers":[{"id":"default","base_url":"https://openrouter.ai/api/v1","api_key":""}],"summary":{"enabled":false}}`)))
	out = decodeLLMSettings(t, rec)
	if out.Providers[0].APIKeyConfigured {
		t.Fatalf("api_key \"\" should clear the key: %+v", out.Providers)
	}
}

func TestPutLLMSettingsRejectsEnabledStepWithoutProvider(t *testing.T) {
	clearLLMEnv(t)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(`{"summary":{"enabled":true}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(rt.llmSettingsPath + ".tmp"); err == nil {
		t.Fatalf("rejected update left a temp file behind")
	}
}

func TestPutLLMSettingsAssignsProviderIDs(t *testing.T) {
	clearLLMEnv(t)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(
		`{"providers":[{"name":"Local","base_url":"http://qwen.internal:8000/v1"}]}`)))
	out := decodeLLMSettings(t, rec)
	if len(out.Providers) != 1 || !regexp.MustCompile(`^p-[0-9a-f]{8}$`).MatchString(out.Providers[0].ID) {
		t.Fatalf("providers = %+v, want a generated id", out.Providers)
	}
}

func TestLLMProviderModelsProxiesDiscovery(t *testing.T) {
	clearLLMEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-secret" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"zeta"},{"id":"alpha","name":"Alpha","context_length":8192},{"id":"zeta"}]}`))
	}))
	defer upstream.Close()
	t.Setenv(envLLMBaseURL, upstream.URL+"/v1")
	t.Setenv(envLLMAPIKey, "sk-or-secret")
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm/providers/default/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out llmModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Provider != "default" || len(out.Models) != 2 || out.Models[0].ID != "alpha" || out.Models[0].ContextLength != 8192 || out.Models[1].ID != "zeta" {
		t.Fatalf("models = %+v", out)
	}

	rec = httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm/providers/nope/models", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d", rec.Code)
	}
}

func TestLLMProviderModelsReportsUpstreamFailure(t *testing.T) {
	clearLLMEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	t.Setenv(envLLMBaseURL, upstream.URL)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm/providers/default/models", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "HTTP 500") {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}
