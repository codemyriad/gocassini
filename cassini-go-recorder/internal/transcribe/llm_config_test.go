package transcribe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A self-hosted OpenAI-compatible server usually has no API key. Requiring one
// used to disable summaries entirely for those endpoints,
// silently — the base URL is what says "an endpoint exists".
func TestIsConfiguredDoesNotRequireAPIKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  LLMConfig
		want bool
	}{
		{"keyless local endpoint", LLMConfig{BaseURL: "http://qwen.internal:8000/v1"}, true},
		{"hosted endpoint with key", LLMConfig{BaseURL: "https://openrouter.ai/api/v1", APIKey: "k"}, true},
		{"key but no endpoint", LLMConfig{APIKey: "k"}, false},
		{"nothing set", LLMConfig{}, false},
		{"explicitly disabled", LLMConfig{BaseURL: "http://qwen.internal:8000/v1", Disabled: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsConfigured(); got != tc.want {
				t.Fatalf("IsConfigured() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultBuildConfigKeylessBaseURLEnablesSummary(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")

	cfg := DefaultBuildConfig()
	if !cfg.SummaryLLM.IsConfigured() {
		t.Error("expected summary to be configured from a keyless base URL")
	}
	if cfg.SummaryLLM.APIKey != "" {
		t.Errorf("expected no API key, got %q", cfg.SummaryLLM.APIKey)
	}
}

func TestDefaultBuildConfigRequestBoundOverrides(t *testing.T) {
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "1800")
	t.Setenv("CASSINI_LLM_MAX_TOKENS", "16384")

	cfg := DefaultBuildConfig()
	if cfg.SummaryLLM.TimeoutSec != 1800 {
		t.Errorf("TimeoutSec = %d, want 1800", cfg.SummaryLLM.TimeoutSec)
	}
	if cfg.SummaryLLM.MaxTokens != 16384 {
		t.Errorf("MaxTokens = %d, want 16384", cfg.SummaryLLM.MaxTokens)
	}
}

func TestDefaultBuildConfigRequestBoundDefaults(t *testing.T) {
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "")
	t.Setenv("CASSINI_LLM_MAX_TOKENS", "")

	cfg := DefaultBuildConfig()
	if cfg.SummaryLLM.TimeoutSec != defaultLLMTimeoutSec {
		t.Errorf("TimeoutSec = %d, want %d", cfg.SummaryLLM.TimeoutSec, defaultLLMTimeoutSec)
	}
	if cfg.SummaryLLM.MaxTokens != defaultLLMMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", cfg.SummaryLLM.MaxTokens, defaultLLMMaxTokens)
	}
}

// An empty "Authorization: Bearer " header is not ignored by every server; some
// self-hosted ones reject the request outright. Send it only when there is a key.
func TestChatCompletionOmitsAuthorizationWhenKeyless(t *testing.T) {
	cases := []struct {
		name       string
		apiKey     string
		wantHeader string
	}{
		{"keyless", "", ""},
		{"with key", "secret", "Bearer secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotHeader string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("Authorization")
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			t.Cleanup(srv.Close)

			cfg := LLMConfig{BaseURL: srv.URL, Model: "test-model", APIKey: tc.apiKey, MaxTokens: 123}
			got, err := ChatCompletion(context.Background(), cfg, "sys", "usr")
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if got != "ok" {
				t.Fatalf("content = %q, want %q", got, "ok")
			}
			if gotHeader != tc.wantHeader {
				t.Errorf("Authorization = %q, want %q", gotHeader, tc.wantHeader)
			}
			if n, ok := gotBody["max_tokens"].(float64); !ok || int(n) != 123 {
				t.Errorf("max_tokens = %v, want 123", gotBody["max_tokens"])
			}
		})
	}
}

func TestChatCompletionFallsBackToDefaultMaxTokens(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := ChatCompletion(context.Background(), LLMConfig{BaseURL: srv.URL, Model: "m"}, "sys", "usr"); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if n, ok := gotBody["max_tokens"].(float64); !ok || int(n) != defaultLLMMaxTokens {
		t.Errorf("max_tokens = %v, want %d", gotBody["max_tokens"], defaultLLMMaxTokens)
	}
}

func clearStepEndpointEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SUMMARY_BASE_URL", "SUMMARY_API_KEY", "SUMMARY_MODEL",
		"SUMMARY_TIMEOUT_SEC", "SUMMARY_MAX_TOKENS", "CASSINI_SUMMARY_DISABLED",
		"INSIGHT_BASE_URL", "INSIGHT_API_KEY", "INSIGHT_MODEL",
		"INSIGHT_TIMEOUT_SEC", "INSIGHT_MAX_TOKENS",
	} {
		t.Setenv(key, "")
	}
}

func TestDefaultBuildConfigSummaryEndpointBringsItsOwnKey(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "https://openrouter.ai/api/v1")
	t.Setenv("OPENROUTER_API_KEY", "hosted-key")
	t.Setenv("LLM_MODEL", "openai/gpt-4o-mini")
	t.Setenv("SUMMARY_BASE_URL", "http://qwen.internal:8000/v1")

	cfg := DefaultBuildConfig()

	if cfg.SummaryLLM.BaseURL != "http://qwen.internal:8000/v1" {
		t.Fatalf("SUMMARY_BASE_URL should override the shared endpoint, got %q", cfg.SummaryLLM.BaseURL)
	}
	if cfg.SummaryLLM.APIKey != "" {
		t.Fatalf("the shared key must not follow the summary step to a different host, got %q", cfg.SummaryLLM.APIKey)
	}
	if cfg.SummaryLLM.Model != "openai/gpt-4o-mini" {
		t.Fatalf("summary should inherit the shared model, got %q", cfg.SummaryLLM.Model)
	}
	if !cfg.SummaryLLM.IsConfigured() {
		t.Fatalf("summary should be configured, got %+v", cfg.SummaryLLM)
	}
}

func TestDefaultBuildConfigSummaryEndpointAloneConfigures(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("SUMMARY_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("SUMMARY_API_KEY", "local-key")

	cfg := DefaultBuildConfig()

	if !cfg.SummaryLLM.IsConfigured() || cfg.SummaryLLM.BaseURL != "http://qwen.internal:8000/v1" || cfg.SummaryLLM.APIKey != "local-key" {
		t.Fatalf("summary should be configured from its own endpoint, got %+v", cfg.SummaryLLM)
	}
}

// --- D-719: the insight step's own endpoint ---

// A deployment that only ever configured a summary endpoint must keep its
// ability to run an insight, so SUMMARY_* is the fallback INSIGHT_* layers over.
func TestDefaultInsightLLMConfigFallsBackToTheSummaryEndpoint(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("SUMMARY_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("SUMMARY_API_KEY", "local-key")
	t.Setenv("SUMMARY_MODEL", "qwen3-8b")
	t.Setenv("SUMMARY_TIMEOUT_SEC", "3600")

	cfg := DefaultInsightLLMConfig()

	if !cfg.IsConfigured() || cfg.BaseURL != "http://qwen.internal:8000/v1" || cfg.APIKey != "local-key" {
		t.Fatalf("insight = %+v, want the summary endpoint", cfg)
	}
	if cfg.Model != "qwen3-8b" || cfg.TimeoutSec != 3600 {
		t.Fatalf("insight = %+v, want the summary model and leash", cfg)
	}
}

func TestDefaultInsightLLMConfigOwnEndpointBringsItsOwnKey(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "https://openrouter.ai/api/v1")
	t.Setenv("OPENROUTER_API_KEY", "hosted-key")
	t.Setenv("SUMMARY_BASE_URL", "https://summary.example/v1")
	t.Setenv("SUMMARY_API_KEY", "summary-key")
	t.Setenv("INSIGHT_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("INSIGHT_MODEL", "qwen3-30b")
	t.Setenv("INSIGHT_TIMEOUT_SEC", "7200")
	t.Setenv("INSIGHT_MAX_TOKENS", "32768")

	cfg := DefaultInsightLLMConfig()

	if cfg.BaseURL != "http://qwen.internal:8000/v1" || cfg.Model != "qwen3-30b" {
		t.Fatalf("insight = %+v, want its own endpoint and model", cfg)
	}
	// Two keys were in scope and neither may follow the step to a third host.
	if cfg.APIKey != "" {
		t.Fatalf("a key the insight endpoint was not given followed it: %q", cfg.APIKey)
	}
	if cfg.TimeoutSec != 7200 || cfg.MaxTokens != 32768 {
		t.Fatalf("insight bounds = %d/%d, want 7200/32768", cfg.TimeoutSec, cfg.MaxTokens)
	}
}

// A model override alone keeps the endpoint it is layered over — the same rule
// the summary step has always had.
func TestDefaultInsightLLMConfigModelAloneKeepsTheEndpoint(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("OPENROUTER_API_KEY", "shared-key")
	t.Setenv("INSIGHT_MODEL", "qwen3-30b")

	cfg := DefaultInsightLLMConfig()

	if cfg.BaseURL != "http://qwen.internal:8000/v1" || cfg.APIKey != "shared-key" {
		t.Fatalf("insight = %+v, want the shared endpoint and its key", cfg)
	}
	if cfg.Model != "qwen3-30b" {
		t.Fatalf("model = %q, want the insight override", cfg.Model)
	}
}

// CASSINI_SUMMARY_DISABLED says "publish meetings without a summary", not
// "refuse a document someone asked for by name".
func TestDefaultInsightLLMConfigIgnoresTheSummaryKillSwitch(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_SUMMARY_DISABLED", "1")

	if cfg := DefaultBuildConfig(); cfg.SummaryLLM.IsConfigured() {
		t.Fatalf("summary should be off, got %+v", cfg.SummaryLLM)
	}
	if cfg := DefaultInsightLLMConfig(); !cfg.IsConfigured() {
		t.Fatalf("insight should still be configured, got %+v", cfg)
	}
}

func TestDefaultBuildConfigSummaryStepBounds(t *testing.T) {
	clearStepEndpointEnv(t)
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "900")
	t.Setenv("CASSINI_LLM_MAX_TOKENS", "4096")
	t.Setenv("SUMMARY_TIMEOUT_SEC", "3600")
	t.Setenv("SUMMARY_MAX_TOKENS", "16384")

	cfg := DefaultBuildConfig().SummaryLLM
	if cfg.TimeoutSec != 3600 || cfg.MaxTokens != 16384 {
		t.Fatalf("summary bounds = %d/%d, want the step's own 3600/16384", cfg.TimeoutSec, cfg.MaxTokens)
	}
}
