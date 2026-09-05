package transcribe

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

//go:embed templates/summary.v0.md
var summaryV0Template string

//go:embed templates/summary-prompt.v0.md
var summaryV0Prompt string

//go:embed templates/summary-local-rules.v0.md
var summaryLocalRules string

// summaryTemplatePlaceholder is the literal token in summary-prompt.v0.md
// where the V0 structure template is spliced in at runtime. If the prompt
// file ever stops containing this token, summarySystemPrompt becomes a no-op
// for the template and the existing TestSummarySystemPromptPinsTemplateAndRules
// test will fail because the headings disappear from the prompt.
const summaryTemplatePlaceholder = "{{TEMPLATE}}"

// BuildMeetingSummary calls the LLM to produce a meeting summary that follows
// the V0 markdown template. Speaker labels from streams are used so the model
// can attribute action items by name. Returns the raw markdown body to be
// written to summary.md next to other meeting artifacts.
func BuildMeetingSummary(cfg LLMConfig, streams []AudioStream, segments []Segment) (string, error) {
	if !cfg.IsConfigured() {
		return "", fmt.Errorf("LLM not configured")
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("no transcript segments to summarize")
	}
	if cfg.Backend != "" && cfg.Backend != "remote" && cfg.Backend != "local" {
		return "", fmt.Errorf("unknown summary backend %q", cfg.Backend)
	}

	systemPrompt := summarySystemPrompt(summaryV0Template)
	userPrompt := formatTranscriptForSummary(streams, segments)
	if cfg.Backend == "local" {
		return localMeetingSummary(cfg, systemPrompt+summaryLocalRules, userPrompt, os.Stderr)
	}

	body, err := chatCompletion(cfg, systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}

	body = stripMarkdownFences(strings.TrimSpace(body))
	if body == "" {
		return "", fmt.Errorf("LLM returned an empty summary")
	}
	return body + "\n", nil
}

func summarySystemPrompt(template string) string {
	return strings.Replace(summaryV0Prompt, summaryTemplatePlaceholder, template, 1)
}

func formatTranscriptForSummary(streams []AudioStream, segments []Segment) string {
	var sb strings.Builder
	for _, seg := range segments {
		label := labelForSpeaker(seg.SpeakerID, streams)
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "%s: %s\n", label, text)
	}
	return sb.String()
}

// stripMarkdownFences removes a single surrounding ```...``` fence if the
// model returned one despite being told not to. Inner fences are left alone.
func stripMarkdownFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	rest = strings.TrimRight(rest, " \n\t")
	rest = strings.TrimSuffix(rest, "```")
	return strings.TrimSpace(rest)
}
