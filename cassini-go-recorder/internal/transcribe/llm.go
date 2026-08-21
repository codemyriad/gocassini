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

// LLMConfig holds settings for an OpenAI-compatible chat completions endpoint,
// used for readable transcript cleanup and meeting summaries.
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

// ReadableCleanup sends transcript segments to the LLM and returns a version
// with cleaned text. Word timestamps are preserved from the original.
// Segments are sent in batches to stay within context limits.
func ReadableCleanup(cfg LLMConfig, segments []Segment) ([]Segment, error) {
	if !cfg.IsConfigured() {
		return nil, fmt.Errorf("LLM not configured")
	}

	const batchChars = 6000
	const batchRecords = 40

	out := make([]Segment, len(segments))
	copy(out, segments)

	start := 0
	for start < len(segments) {
		end := start
		chars := 0
		for end < len(segments) && (end-start) < batchRecords && chars < batchChars {
			chars += len(segments[end].Text)
			end++
		}

		batch := segments[start:end]
		cleaned, err := cleanupBatch(cfg, batch)
		if err != nil {
			return nil, fmt.Errorf("batch %d-%d: %w", start, end, err)
		}
		for i, text := range cleaned {
			out[start+i].Text = text
		}
		start = end
	}
	return out, nil
}

func cleanupBatch(cfg LLMConfig, batch []Segment) ([]string, error) {
	var sb strings.Builder
	for i, seg := range batch {
		fmt.Fprintf(&sb, "@@%d@@ %s\n", i, seg.Text)
	}

	systemPrompt := "You are a transcript editor. Rewrite spoken meeting transcript text into clean, readable prose. Fix grammar, remove filler words (uh, um, like), correct obvious transcription errors. Preserve meaning and speaker intent exactly. Return ONLY the rewritten records using the exact @@index@@ format. No markdown, no JSON, no extra commentary."
	userPrompt := sb.String()

	respText, err := chatCompletion(cfg, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	results := make([]string, len(batch))
	for i := range results {
		results[i] = batch[i].Text // fallback: keep original
	}

	for _, line := range strings.Split(respText, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		end := strings.Index(line[2:], "@@")
		if end < 0 {
			continue
		}
		idxStr := line[2 : 2+end]
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err != nil {
			continue
		}
		if idx < 0 || idx >= len(batch) {
			continue
		}
		text := strings.TrimSpace(line[2+end+2:])
		if text != "" {
			results[idx] = text
		}
	}
	return results, nil
}

func chatCompletion(cfg LLMConfig, system, user string) (string, error) {
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
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
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
