package operator

import (
	"bytes"
	"encoding/json"
	"log"
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
	if p.ID != "default" || p.Name != "Endpoint 1" || p.BaseURL != "http://qwen.internal:8000/v1" || p.APIKey != "" {
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
	if p.BaseURL != openRouterBaseURL || p.Name != "Endpoint 1" || p.APIKey != "sk-or-secret" {
		t.Fatalf("provider = %+v", p)
	}
}

// A provider's display name is the one field every signed-in user sees, so an
// unnamed one gets its position and never its host: "Endpoint 2", not
// "qwen.internal:8000" (D-740).
func TestUnnamedProvidersAreNumberedAndNeverNamedAfterTheirHost(t *testing.T) {
	got, err := normalizeLLMSettings(LLMSettings{Providers: []LLMProvider{
		{ID: "hosted", BaseURL: openRouterBaseURL},
		{ID: "local", Name: "  ", BaseURL: "http://qwen.internal:8000/v1"},
		{ID: "named", Name: "Lab box", BaseURL: "http://lab.internal:8000/v1"},
	}})
	if err != nil {
		t.Fatalf("normalizeLLMSettings: %v", err)
	}
	for i, want := range []string{"Endpoint 1", "Endpoint 2", "Lab box"} {
		if got.Providers[i].Name != want {
			t.Errorf("provider %d name = %q, want %q", i, got.Providers[i].Name, want)
		}
	}

	// And the USER route, which is where the name goes, carries no host.
	clearLLMEnv(t)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.setLLMSettings(got)
	rec := httptest.NewRecorder()
	rt.aiProvidersHandler(rec, httptest.NewRequest(http.MethodGet, "/ai/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, host := range []string{"openrouter.ai", "qwen.internal", "lab.internal"} {
		if strings.Contains(rec.Body.String(), host) {
			t.Errorf("GET /ai/providers names a host (%s): %s", host, rec.Body.String())
		}
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
	if s.Providers[0].TimeoutSec != 1800 || s.Providers[0].MaxTokens != 8192 {
		t.Fatalf("provider bounds = %d/%d, want 1800/8192", s.Providers[0].TimeoutSec, s.Providers[0].MaxTokens)
	}
	view := s.view()
	if view.Effective.Summary != nil {
		t.Fatalf("effective summary = %+v, want disabled", view.Effective.Summary)
	}
	if got := view.Effective.Insight; got == nil || !got.Inherited || got.Provider != "default" || got.Model != "large" {
		t.Fatalf("effective insight = %+v, want the disabled summary's provider and model inherited", got)
	}
	env := s.ChildEnv(nil)
	for key, want := range map[string]string{
		"INSIGHT_BASE_URL":    "http://qwen.internal:8000/v1",
		"INSIGHT_MODEL":       "large",
		"INSIGHT_TIMEOUT_SEC": "1800",
		"INSIGHT_MAX_TOKENS":  "8192",
	} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("%s = %q (present=%v), want %q; env=%v", key, got, ok, want, env)
		}
	}
	if _, ok := envValue(env, "SUMMARY_BASE_URL"); ok {
		t.Fatalf("disabled summary endpoint leaked into env: %v", env)
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
			{ID: "hosted", Name: "OpenRouter", BaseURL: openRouterBaseURL, APIKey: "sk-or-secret", TimeoutSec: 600},
			{ID: "local", Name: "Qwen", BaseURL: "http://qwen.internal:8000/v1"},
		},
		Summary: LLMStep{Enabled: true, Provider: "hosted", Model: "big"},
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
		"INSIGHT_BASE_URL=https://stale.example/v1",
		"INSIGHT_API_KEY=stale-key",
	}
	env := s.ChildEnv(base)

	want := map[string]string{
		"PATH":                "/bin",
		"SUMMARY_BASE_URL":    openRouterBaseURL,
		"SUMMARY_API_KEY":     "sk-or-secret",
		"SUMMARY_MODEL":       "big",
		"SUMMARY_TIMEOUT_SEC": "600",
		// The insight step has no endpoint of its own, so it runs on the
		// summary one — emitted in full rather than left to the recorder's
		// layering, so the deploy environment's stale INSIGHT_* is replaced
		// rather than merely uncontradicted.
		"INSIGHT_BASE_URL":    openRouterBaseURL,
		"INSIGHT_API_KEY":     "sk-or-secret",
		"INSIGHT_MODEL":       "big",
		"INSIGHT_TIMEOUT_SEC": "600",
	}
	for key, value := range want {
		if got, ok := envValue(env, key); !ok || got != value {
			t.Fatalf("%s = %q (present=%v), want %q; env=%v", key, got, ok, value, env)
		}
	}
	for _, key := range []string{
		"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "LLM_BASE_URL", "LLM_MODEL",
		"CASSINI_SUMMARY_DISABLED", "CASSINI_READABLE_DISABLED", "CASSINI_LLM_MAX_TOKENS",
		"CASSINI_LLM_TIMEOUT_SEC", "READABLE_BASE_URL", "READABLE_API_KEY", "READABLE_MODEL",
	} {
		if got, ok := envValue(env, key); ok {
			t.Fatalf("%s should be absent, got %q", key, got)
		}
	}
	if len(env) != len(want) {
		t.Fatalf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
}

func TestLLMChildEnvDisabledSummaryStillSuppliesInheritedInsight(t *testing.T) {
	s := LLMSettings{
		Providers: []LLMProvider{{ID: "local", BaseURL: "http://qwen.internal:8000/v1"}},
		Summary:   LLMStep{Enabled: false, Provider: "local", Model: "cheap"},
	}
	env := s.ChildEnv(nil)
	if _, ok := envValue(env, "SUMMARY_BASE_URL"); ok {
		t.Fatalf("disabled summary step leaked into env: %v", env)
	}
	if got, ok := envValue(env, "INSIGHT_BASE_URL"); !ok || got != "http://qwen.internal:8000/v1" {
		t.Fatalf("INSIGHT_BASE_URL = %q (present=%v), want the selected summary provider; env=%v", got, ok, env)
	}
	if got, ok := envValue(env, "INSIGHT_MODEL"); !ok || got != "cheap" {
		t.Fatalf("INSIGHT_MODEL = %q (present=%v), want inherited summary model; env=%v", got, ok, env)
	}
	if got, ok := envValue(env, "INSIGHT_API_KEY"); ok {
		t.Fatalf("keyless inherited endpoint got a key: %q", got)
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
		"negative timeout":              {Providers: []LLMProvider{{ID: "x", BaseURL: "http://qwen.internal/v1", TimeoutSec: -1}}},
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
	if len(out.Providers) != 1 || !out.Providers[0].APIKeyConfigured || out.Providers[0].Name != "Endpoint 1" {
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

// The USER twin of that route is read by everybody who is signed in, and a
// transport error's text carries the endpoint's full URL. The base URL is
// administrator-only on every other surface, so the USER route says only what
// kind of failure it was; the detail goes to the log and stays on the ADMIN
// route (D-740).
func TestAIProviderModelsTellsAUserNothingAboutTheEndpoint(t *testing.T) {
	clearLLMEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	secretHost := strings.TrimPrefix(upstream.URL, "http://")

	t.Run("the endpoint answered with an error", func(t *testing.T) {
		t.Setenv(envLLMBaseURL, upstream.URL+"/v1/private-path")
		var logs bytes.Buffer
		rt, cleanup := newTestRuntimeWithLogger(t, log.New(&logs, "", 0))
		defer cleanup()

		rec := httptest.NewRecorder()
		rt.aiProviderModelsHandler(rec, httptest.NewRequest(http.MethodGet, "/ai/providers/default/models", nil))
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "the endpoint returned HTTP 500") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), secretHost) || strings.Contains(rec.Body.String(), "private-path") {
			t.Fatalf("the USER route named the endpoint: %s", rec.Body.String())
		}
	})

	t.Run("the endpoint could not be reached", func(t *testing.T) {
		// A closed server: the *url.Error this produces prints the whole URL.
		gone := httptest.NewServer(http.NotFoundHandler())
		goneURL := gone.URL
		gone.Close()
		t.Setenv(envLLMBaseURL, goneURL+"/v1/private-path")
		var logs bytes.Buffer
		rt, cleanup := newTestRuntimeWithLogger(t, log.New(&logs, "", 0))
		defer cleanup()

		rec := httptest.NewRecorder()
		rt.aiProviderModelsHandler(rec, httptest.NewRequest(http.MethodGet, "/ai/providers/default/models", nil))
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "the endpoint could not be reached") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "private-path") || strings.Contains(rec.Body.String(), strings.TrimPrefix(goneURL, "http://")) {
			t.Fatalf("the USER route named the endpoint: %s", rec.Body.String())
		}
		if !strings.Contains(logs.String(), "private-path") {
			t.Fatalf("the detail must still reach the log for an administrator: %s", logs.String())
		}

		// The ADMIN twin keeps the detail, because that is who fixes it.
		rec = httptest.NewRecorder()
		rt.llmSettingsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/llm/providers/default/models", nil))
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "private-path") {
			t.Fatalf("ADMIN status = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// --- D-719: the insight step ---

func TestLLMChildEnvGivesEachStepItsOwnEndpoint(t *testing.T) {
	s := LLMSettings{
		Providers: []LLMProvider{
			{ID: "hosted", BaseURL: openRouterBaseURL, APIKey: "sk-or-secret", TimeoutSec: 120},
			{ID: "local", BaseURL: "http://qwen.internal:8000/v1", TimeoutSec: 3600, MaxTokens: 16384},
		},
		Summary: LLMStep{Enabled: true, Provider: "local", Model: "qwen3-8b"},
		Insight: LLMStep{Enabled: true, Provider: "hosted", Model: "anthropic/claude-sonnet-4.5"},
	}
	env := s.ChildEnv(nil)

	want := map[string]string{
		"SUMMARY_BASE_URL":    "http://qwen.internal:8000/v1",
		"SUMMARY_MODEL":       "qwen3-8b",
		"SUMMARY_TIMEOUT_SEC": "3600",
		"SUMMARY_MAX_TOKENS":  "16384",
		"INSIGHT_BASE_URL":    openRouterBaseURL,
		"INSIGHT_API_KEY":     "sk-or-secret",
		"INSIGHT_MODEL":       "anthropic/claude-sonnet-4.5",
		"INSIGHT_TIMEOUT_SEC": "120",
	}
	for key, value := range want {
		if got, ok := envValue(env, key); !ok || got != value {
			t.Fatalf("%s = %q (present=%v), want %q; env=%v", key, got, ok, value, env)
		}
	}
	// The keyless local endpoint must not pick up the hosted provider's key,
	// and the two steps' bounds must not collide on one shared variable.
	if got, ok := envValue(env, "SUMMARY_API_KEY"); ok {
		t.Fatalf("keyless summary endpoint got a key: %q", got)
	}
	if len(env) != len(want) {
		t.Fatalf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
}

func TestLLMInsightWithNoEndpointOfItsOwnInheritsSummary(t *testing.T) {
	s := LLMSettings{
		Providers: []LLMProvider{{ID: "local", BaseURL: "http://qwen.internal:8000/v1"}},
		Summary:   LLMStep{Enabled: true, Provider: "local", Model: "qwen3-8b"},
	}
	// The summary endpoint is emitted for the insight step by name rather than
	// left to the recorder's INSIGHT_*-over-SUMMARY_* layering, so what the
	// child receives says the same thing the settings view does.
	env := s.ChildEnv(nil)
	if got, ok := envValue(env, "INSIGHT_BASE_URL"); !ok || got != "http://qwen.internal:8000/v1" {
		t.Fatalf("INSIGHT_BASE_URL = %q (present=%v), want the summary endpoint", got, ok)
	}
	if got, ok := envValue(env, "INSIGHT_MODEL"); !ok || got != "qwen3-8b" {
		t.Fatalf("INSIGHT_MODEL = %q (present=%v), want the summary model", got, ok)
	}
	effective := s.view().Effective.Insight
	if effective == nil {
		t.Fatal("effective insight is nil, which reads as 'insights are off' when they are not")
	}
	if effective.BaseURL != "http://qwen.internal:8000/v1" || !effective.Inherited {
		t.Fatalf("effective insight = %+v, want the summary endpoint marked inherited", effective)
	}
	if summary := s.view().Effective.Summary; summary == nil || summary.Inherited {
		t.Fatalf("effective summary = %+v, want its own endpoint, not inherited", summary)
	}
}

// A registered provider is the whole of the setup an insight needs. Neither
// step is switched on here — an administrator who saved a key and left every
// meeting unsummarised — and the question they type by hand still has somewhere
// to go.
func TestLLMInsightRunsOnASavedProviderWithNeitherStepOn(t *testing.T) {
	s := LLMSettings{Providers: []LLMProvider{
		{ID: "local", BaseURL: "http://qwen.internal:8000/v1", APIKey: "sk-local"},
	}}
	effective := s.view().Effective.Insight
	if effective == nil {
		t.Fatal("effective insight is nil with an endpoint registered, which is the state that reads as 'no AI endpoint is available'")
	}
	if effective.BaseURL != "http://qwen.internal:8000/v1" || !effective.Inherited {
		t.Fatalf("effective insight = %+v, want the saved provider marked inherited", effective)
	}
	env := s.ChildEnv(nil)
	if got, ok := envValue(env, "INSIGHT_BASE_URL"); !ok || got != "http://qwen.internal:8000/v1" {
		t.Fatalf("INSIGHT_BASE_URL = %q (present=%v), want the saved provider", got, ok)
	}
	if got, ok := envValue(env, "INSIGHT_API_KEY"); !ok || got != "sk-local" {
		t.Fatalf("INSIGHT_API_KEY = %q (present=%v), want the saved provider's key", got, ok)
	}
	// Summarising is a separate opt-in and stays off: the fallback unlocks the
	// question somebody asks for, not the step that runs on every recording.
	if summary := s.view().Effective.Summary; summary != nil {
		t.Fatalf("effective summary = %+v, want nil with the step off", summary)
	}
	if got, ok := envValue(env, "SUMMARY_BASE_URL"); ok {
		t.Fatalf("SUMMARY_BASE_URL = %q, want absent with the step off", got)
	}
}

func TestLLMEffectiveInsightIsNilWhenNoProviderExists(t *testing.T) {
	s := LLMSettings{}
	if got := s.view().Effective.Insight; got != nil {
		t.Fatalf("effective insight = %+v, want nil with no provider registered", got)
	}
}

// A settings file written before the insight step existed must load, keep every
// field it carried, and read as "insight inherits the summary endpoint".
func TestLoadLLMSettingsWrittenBeforeTheInsightStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm-settings.json")
	legacy := `{
  "providers": [
    {
      "id": "default",
      "name": "OpenRouter",
      "base_url": "https://openrouter.ai/api/v1",
      "api_key": "sk-or-persisted",
      "timeout_sec": 1800,
      "max_tokens": 8192
    }
  ],
  "summary": {
    "enabled": true,
    "provider": "default",
    "model": "openai/gpt-4o-mini"
  }
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}
	// The deploy environment must not be consulted for an existing file, so a
	// hostile one here would show up as a changed policy.
	loaded, err := LoadOrInitLLMSettings(path, llmGetenv(map[string]string{envLLMBaseURL: "http://seeded.example/v1"}))
	if err != nil {
		t.Fatalf("LoadOrInitLLMSettings() error = %v", err)
	}
	if len(loaded.Providers) != 1 {
		t.Fatalf("providers = %+v, want the one persisted", loaded.Providers)
	}
	p := loaded.Providers[0]
	if p.ID != "default" || p.APIKey != "sk-or-persisted" || p.TimeoutSec != 1800 || p.MaxTokens != 8192 {
		t.Fatalf("provider = %+v, want every persisted field kept", p)
	}
	if want := (LLMStep{Enabled: true, Provider: "default", Model: "openai/gpt-4o-mini"}); loaded.Summary != want {
		t.Fatalf("summary = %+v, want %+v", loaded.Summary, want)
	}
	if (loaded.Insight != LLMStep{}) {
		t.Fatalf("insight = %+v, want the zero step so it inherits the summary endpoint", loaded.Insight)
	}
	if effective := loaded.view().Effective.Insight; effective == nil || !effective.Inherited {
		t.Fatalf("effective insight = %+v, want the inherited summary endpoint", effective)
	}

	// Saving it back must not drop anything the old file carried.
	if err := SaveLLMSettings(path, loaded); err != nil {
		t.Fatalf("SaveLLMSettings() error = %v", err)
	}
	reloaded, err := LoadOrInitLLMSettings(path, llmGetenv(nil))
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if reloaded.Summary != loaded.Summary || len(reloaded.Providers) != 1 || reloaded.Providers[0] != p {
		t.Fatalf("reloaded = %+v, want a lossless round trip of %+v", reloaded, loaded)
	}
}

func TestPutLLMSettingsStoresTheInsightStepAndItsTemplate(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv(envLLMBaseURL, "http://qwen.internal:8000/v1")
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	body := `{
	  "providers": [
	    {"id": "default", "name": "Qwen", "base_url": "http://qwen.internal:8000/v1"},
	    {"id": "hosted", "name": "OpenRouter", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or-secret"}
	  ],
	  "summary": {"enabled": true, "provider": "default", "model": "qwen3-8b", "template": "summarise"},
	  "insight": {"enabled": true, "provider": "hosted", "model": "big", "template": "decisions"}
	}`
	req := httptest.NewRequest(http.MethodPut, "/settings/llm", strings.NewReader(body))
	rec := httptest.NewRecorder()
	rt.llmSettingsHandler(rec, req)
	out := decodeLLMSettings(t, rec)

	if strings.Contains(rec.Body.String(), "sk-or-secret") {
		t.Fatalf("PUT echoed the key: %s", rec.Body.String())
	}
	if out.Summary.Template != "summarise" || out.Insight.Template != "decisions" {
		t.Fatalf("templates = %q / %q, want summarise / decisions", out.Summary.Template, out.Insight.Template)
	}
	if out.Effective.Insight == nil || out.Effective.Insight.BaseURL != openRouterBaseURL || out.Effective.Insight.Inherited {
		t.Fatalf("effective insight = %+v, want its own hosted endpoint", out.Effective.Insight)
	}
	if out.Effective.Summary == nil || out.Effective.Summary.BaseURL != "http://qwen.internal:8000/v1" {
		t.Fatalf("effective summary = %+v, want the local endpoint", out.Effective.Summary)
	}

	// The two steps really do reach two different hosts, with only the hosted
	// one carrying the key.
	env := rt.currentLLMSettings().ChildEnv(nil)
	if got, _ := envValue(env, "SUMMARY_BASE_URL"); got != "http://qwen.internal:8000/v1" {
		t.Fatalf("SUMMARY_BASE_URL = %q", got)
	}
	if got, _ := envValue(env, "INSIGHT_API_KEY"); got != "sk-or-secret" {
		t.Fatalf("INSIGHT_API_KEY = %q", got)
	}
	if got, ok := envValue(env, "SUMMARY_API_KEY"); ok {
		t.Fatalf("the keyless local endpoint was handed a key: %q", got)
	}
}

func TestNormalizeLLMSettingsRejectsUnusableTemplate(t *testing.T) {
	local := LLMProvider{ID: "local", BaseURL: "http://qwen.internal:8000/v1"}
	cases := map[string]LLMSettings{
		// The id ends up on a `cassini insight run --workflow` command line.
		"template with a space": {Providers: []LLMProvider{local}, Summary: LLMStep{Template: "two words"}},
		"template with a slash": {Providers: []LLMProvider{local}, Insight: LLMStep{Template: "../etc/passwd"}},
		"template as a flag":    {Providers: []LLMProvider{local}, Insight: LLMStep{Template: "--out"}},
		"insight unknown provider": {
			Providers: []LLMProvider{local},
			Insight:   LLMStep{Enabled: true, Provider: "gone"},
		},
	}
	for name, in := range cases {
		if _, err := normalizeLLMSettings(in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// A template on a step with no provider is fine: naming the workflow and
	// choosing the endpoint are separate decisions.
	got, err := normalizeLLMSettings(LLMSettings{Insight: LLMStep{Template: "  decisions  "}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Insight.Template != "decisions" {
		t.Fatalf("template = %q, want it trimmed", got.Insight.Template)
	}
}

// Registering the FIRST endpoint is the opt-in: a fresh install that has just
// configured the thing and still publishes meetings without summaries has done
// the work and not got the feature.
func TestFirstProviderSwitchesSummarisingOn(t *testing.T) {
	before := LLMSettings{}
	after := LLMSettings{Providers: []LLMProvider{{ID: "p-1", BaseURL: openRouterBaseURL}}}
	got := enableSummaryOnFirstProvider(before, after)
	if !got.Summary.Enabled || got.Summary.Provider != "p-1" {
		t.Fatalf("summary = %+v, want enabled against p-1", got.Summary)
	}
}

// And at most once in a deployment's life. Every one of these is somebody who
// has already made a decision the save must not overturn.
func TestSummarisingIsNotSwitchedBackOn(t *testing.T) {
	existing := []LLMProvider{{ID: "p-1", BaseURL: openRouterBaseURL}}
	for _, tc := range []struct {
		name          string
		before, after LLMSettings
	}{
		{
			// The second endpoint, added by an administrator who deliberately
			// switched summarising off after the first.
			name:   "a second endpoint",
			before: LLMSettings{Providers: existing},
			after: LLMSettings{Providers: append(append([]LLMProvider{}, existing...),
				LLMProvider{ID: "p-2", BaseURL: "http://local:8000/v1"})},
		},
		{
			// Switching it off IS a save, and it carries the providers with it.
			name:   "switching it off",
			before: LLMSettings{Providers: existing, Summary: LLMStep{Enabled: true, Provider: "p-1"}},
			after:  LLMSettings{Providers: existing},
		},
		{
			// Removing the last endpoint. Nothing to point at, and turning a
			// step on against no provider is a body the operator refuses.
			name:   "the last endpoint removed",
			before: LLMSettings{Providers: existing},
			after:  LLMSettings{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := enableSummaryOnFirstProvider(tc.before, tc.after); got.Summary.Enabled {
				t.Fatalf("summary = %+v, want left off", got.Summary)
			}
		})
	}
}

// A first save that asks for summarising OFF explicitly is still honoured: the
// rule fills a gap, it does not overrule an answer.
func TestFirstProviderDoesNotOverruleAnExplicitChoice(t *testing.T) {
	before := LLMSettings{}
	after := LLMSettings{
		Providers: []LLMProvider{{ID: "p-1", BaseURL: openRouterBaseURL}},
		Summary:   LLMStep{Enabled: false, Provider: "p-1"},
	}
	if got := enableSummaryOnFirstProvider(before, after); got.Summary.Enabled {
		t.Fatalf("summary = %+v, want left off — the request named a provider and said no", got.Summary)
	}
}
