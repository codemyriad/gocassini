package transcribe

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A self-hosted OpenAI-compatible server usually has no API key. Requiring one
// used to disable readable cleanup and summaries entirely for those endpoints,
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

func TestDefaultBuildConfigKeylessBaseURLEnablesBothSteps(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")

	cfg := DefaultBuildConfig()
	if !cfg.LLM.IsConfigured() {
		t.Error("expected readable cleanup to be configured from a keyless base URL")
	}
	if !cfg.SummaryLLM.IsConfigured() {
		t.Error("expected summary to be configured from a keyless base URL")
	}
	if cfg.LLM.APIKey != "" {
		t.Errorf("expected no API key, got %q", cfg.LLM.APIKey)
	}
}

// The two kill-switches are independent in both directions: disabling cleanup
// (which we do not use) must not take summaries down with it.
func TestDefaultBuildConfigReadableDisabledToggle(t *testing.T) {
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_READABLE_DISABLED", "1")
	t.Setenv("CASSINI_SUMMARY_DISABLED", "")

	cfg := DefaultBuildConfig()
	if cfg.LLM.IsConfigured() {
		t.Error("expected readable cleanup to be unconfigured when CASSINI_READABLE_DISABLED=1")
	}
	if !cfg.SummaryLLM.IsConfigured() {
		t.Error("expected summary to remain configured when only cleanup is disabled")
	}
}

func TestDefaultBuildConfigRequestBoundOverrides(t *testing.T) {
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "1800")
	t.Setenv("CASSINI_LLM_MAX_TOKENS", "16384")

	cfg := DefaultBuildConfig()
	for _, c := range []struct {
		name string
		llm  LLMConfig
	}{{"LLM", cfg.LLM}, {"SummaryLLM", cfg.SummaryLLM}} {
		if c.llm.TimeoutSec != 1800 {
			t.Errorf("%s.TimeoutSec = %d, want 1800", c.name, c.llm.TimeoutSec)
		}
		if c.llm.MaxTokens != 16384 {
			t.Errorf("%s.MaxTokens = %d, want 16384", c.name, c.llm.MaxTokens)
		}
	}
}

func TestDefaultBuildConfigRequestBoundDefaults(t *testing.T) {
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", "http://qwen.internal:8000/v1")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "")
	t.Setenv("CASSINI_LLM_MAX_TOKENS", "")

	cfg := DefaultBuildConfig()
	if cfg.LLM.TimeoutSec != defaultLLMTimeoutSec {
		t.Errorf("TimeoutSec = %d, want %d", cfg.LLM.TimeoutSec, defaultLLMTimeoutSec)
	}
	if cfg.LLM.MaxTokens != defaultLLMMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", cfg.LLM.MaxTokens, defaultLLMMaxTokens)
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
			got, err := chatCompletion(cfg, "sys", "usr")
			if err != nil {
				t.Fatalf("chatCompletion: %v", err)
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

	if _, err := chatCompletion(LLMConfig{BaseURL: srv.URL, Model: "m"}, "sys", "usr"); err != nil {
		t.Fatalf("chatCompletion: %v", err)
	}
	if n, ok := gotBody["max_tokens"].(float64); !ok || int(n) != defaultLLMMaxTokens {
		t.Errorf("max_tokens = %v, want %d", gotBody["max_tokens"], defaultLLMMaxTokens)
	}
}
