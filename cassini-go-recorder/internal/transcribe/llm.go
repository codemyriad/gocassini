package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMConfig holds settings for an OpenAI-compatible chat completions endpoint,
// used for meeting summaries.
type LLMConfig struct {
	APIKey     string
	BaseURL    string // e.g. "https://openrouter.ai/api/v1"
	Model      string // e.g. "openai/gpt-4o-mini"
	TimeoutSec int
	MaxTokens  int
	// Disabled turns this capability off regardless of the rest of the
	// config. It is explicit rather than inferred from a missing key,
	// because a self-hosted endpoint legitimately has no key.
	Disabled bool
}

// Default request bounds, used when a config leaves them unset. The timeout is
// generous because a CPU-bound local model is far slower than a hosted one.
const (
	defaultLLMTimeoutSec = 900
	defaultLLMMaxTokens  = 4096
)

// DefaultLLMConfig returns an LLMConfig with the default model and request
// bounds. The endpoint fields are left to the caller.
func DefaultLLMConfig() LLMConfig {
	return LLMConfig{
		Model:      "openai/gpt-4o-mini",
		TimeoutSec: defaultLLMTimeoutSec,
		MaxTokens:  defaultLLMMaxTokens,
	}
}

// IsConfigured returns true if the config has enough to make API calls.
//
// An API key is deliberately not required: a self-hosted OpenAI-compatible
// server (llama.cpp, vLLM, Ollama) usually has none, and demanding one here
// silently disabled the whole capability for those endpoints.
func (c LLMConfig) IsConfigured() bool {
	return !c.Disabled && c.BaseURL != ""
}

// Segment is a contiguous block of speech from one speaker, used for LLM cleanup.
type Segment struct {
	SpeakerID string
	StartMS   int64
	EndMS     int64
	Text      string
	Words     []Word
}

// APIError is the endpoint's own refusal: it answered, and the answer was no.
//
// Typed rather than formatted into a string because a caller has to be able to
// tell "the provider said no" — a missing key, a quota, a model that does not
// exist — from "the call never completed", which is a timeout or a dead host.
// Those need different words in front of a user and, for `cassini insight run`,
// different exit codes. The message is unchanged from the fmt.Errorf it
// replaced, so nothing that reads the text sees a difference.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.StatusCode, e.Body)
}

// ChatCompletion sends one system and user message to the configured
// OpenAI-compatible endpoint and returns the reply.
//
// Exported because the meeting summary is no longer the only caller: the
// insight seam (D-656) assembles its own prompt from a context bundle and has
// no transcript structs to hand BuildMeetingSummary. Sending prompt text is the
// smallest thing that seam needs from this package, so it is the only thing it
// gets.
func ChatCompletion(ctx context.Context, cfg LLMConfig, system, user string) (string, error) {
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultLLMMaxTokens
	}

	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		"max_tokens":  maxTokens,
	})
	if err != nil {
		return "", err
	}

	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultLLMTimeoutSec
	}
	timeout := time.Duration(timeoutSec) * time.Second
	client := &http.Client{Timeout: timeout}

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		// Omitted entirely when unset: some self-hosted servers reject an
		// empty bearer token rather than ignoring it.
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/gocassini")
	req.Header.Set("X-Title", "gocassini")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP POST to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{StatusCode: resp.StatusCode, Body: truncate(string(body), 400)}
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse API response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in API response")
	}
	return result.Choices[0].Message.Content, nil
}
