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
	APIKey     string
	BaseURL    string // e.g. "https://openrouter.ai/api/v1"
	Model      string // e.g. "openai/gpt-4o-mini"
	TimeoutSec int
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
	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		"max_tokens":  4096,
	})
	if err != nil {
		return "", err
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 240 * time.Second
	}
	client := &http.Client{Timeout: timeout}

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

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned %d: %s", resp.StatusCode, truncate(string(body), 400))
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
