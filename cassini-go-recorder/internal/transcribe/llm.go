package transcribe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMConfig holds settings for the meeting-summary API.
type LLMConfig struct {
	Backend     string // ""/remote, local, or off
	ServerPath  string // bundled llama-server executable; local only
	CacheDir    string
	Device      string // cpu or cuda; local only
	ContextSize int
	APIKey      string
	BaseURL     string // e.g. "https://openrouter.ai/api/v1"
	Model       string // e.g. "openai/gpt-4o-mini"
	TimeoutSec  int
}

// DefaultLLMConfig returns an LLMConfig from standard environment variables,
// or an empty config if none are set.
func DefaultLLMConfig() LLMConfig {
	return LLMConfig{
		Model:      "openai/gpt-4o-mini",
		TimeoutSec: 240,
	}
}

// IsConfigured returns true if the config has enough to make API calls.
func (c LLMConfig) IsConfigured() bool {
	if c.Backend == "off" {
		return false
	}
	if c.Backend != "" && c.Backend != "remote" {
		return true
	}
	return c.APIKey != "" && c.BaseURL != ""
}

// Segment is a contiguous block of speech from one speaker.
type Segment struct {
	SpeakerID string
	StartMS   int64
	EndMS     int64
	Text      string
	Words     []Word
}

func chatCompletion(cfg LLMConfig, system, user string) (string, error) {
	payload := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		"max_tokens":  4096,
	}
	if cfg.Backend == "local" {
		payload["chat_template_kwargs"] = map[string]bool{"enable_thinking": false}
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 240 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	if cfg.Backend == "local" {
		client.Transport = &http.Transport{Proxy: nil}
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return fmt.Errorf("local summary server redirected the request")
		}
	}
	defer client.CloseIdleConnections()

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("HTTP-Referer", "https://github.com/gocassini")
	req.Header.Set("X-Title", "gocassini")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP POST to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read API response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned %d: %s", resp.StatusCode, truncate(string(body), 400))
	}

	var result struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
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
	if reason := result.Choices[0].FinishReason; reason != "stop" && (cfg.Backend == "local" || reason != "") {
		return "", fmt.Errorf("summary did not complete (finish_reason=%q)", reason)
	}
	return result.Choices[0].Message.Content, nil
}
