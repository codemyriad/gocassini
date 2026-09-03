package transcribe

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed templates/summary.v0.md
var summaryV0Template string

//go:embed templates/summary-prompt.v0.md
var summaryV0Prompt string

// summaryTemplatePlaceholder is the literal token in summary-prompt.v0.md
// where the V0 structure template is spliced in at runtime. If the prompt
// file ever stops containing this token, summarySystemPrompt becomes a no-op
// for the template and the existing TestSummarySystemPromptPinsTemplateAndRules
// test will fail because the headings disappear from the prompt.
const summaryTemplatePlaceholder = "{{TEMPLATE}}"

// The one workflow this product ships, named and versioned so that a document
// produced by it can say which prompt produced it (D-656).
//
// SummaryWorkflowVersion matches the v0 in the two file names, and is the same
// string already written into a packed meeting's "templateVersion" field. A
// prompt is never edited in place — a change is a new version and a new pair of
// files — because two documents claiming the same version and disagreeing is
// worse than either of them.
const (
	SummaryWorkflowID      = "summarise"
	SummaryWorkflowVersion = "v0"
)

// SummaryPromptV0 and SummaryTemplateV0 hand out the two halves of that prompt.
//
// They exist so the insight seam can hash and splice the exact bytes the
// pipeline sends, rather than keeping a second copy of them. Go's embed cannot
// read outside its own package directory, so the only alternative was a copy
// under internal/insight — and a copy of a prompt is precisely the drift the
// content hash exists to catch, installed deliberately.
//
// The splice happens in the caller: SummaryPromptV0 still carries the
// {{TEMPLATE}} token.
func SummaryPromptV0() string   { return summaryV0Prompt }
func SummaryTemplateV0() string { return summaryV0Template }

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

	systemPrompt := summarySystemPrompt(summaryV0Template)
	userPrompt := formatTranscriptForSummary(streams, segments)

	body, err := ChatCompletion(context.Background(), cfg, systemPrompt, userPrompt)
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
