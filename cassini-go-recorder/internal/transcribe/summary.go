package transcribe

import (
	"context"
	"fmt"
	"strings"

	"gocassini/internal/insight/workflows"
)

// The summary step's prompt is the registry's `summarise` workflow, read from
// the package that owns every prompt this product ships rather than embedded a
// second time here (D-718).
//
// It used to be two //go:embed directives over internal/transcribe/templates/.
// Moving the files was the point: this package links the cgo speech recogniser,
// so prompt bytes embedded here could only be hashed and gated where a speech
// toolchain is installed — and the drift gate has to run in lint.yml, the one
// workflow a prompt-only pull request actually triggers. The bytes are
// unchanged, the splice is unchanged, and the prompt this step sends is now
// literally the prompt `cassini insight run --workflow summarise` sends.
var (
	summaryV0Prompt   = workflows.SummarisePromptV0()
	summaryV0Template = workflows.SummariseTemplateV0()
)

// summaryTemplatePlaceholder is the literal token in the summarise prompt
// where the V0 structure template is spliced in at runtime. If the prompt
// file ever stops containing this token, summarySystemPrompt becomes a no-op
// for the template and the existing TestSummarySystemPromptPinsTemplateAndRules
// test will fail because the headings disappear from the prompt.
const summaryTemplatePlaceholder = "{{TEMPLATE}}"

// The workflow the pipeline's summary step runs, named and versioned so that a
// document produced by it can say which prompt produced it (D-656).
//
// SummaryWorkflowVersion matches the v0 in the two file names, and is the same
// string already written into a packed meeting's "templateVersion" field. A
// prompt is never edited in place — a change is a new version and a new pair of
// files — because two documents claiming the same version and disagreeing is
// worse than either of them.
const (
	SummaryWorkflowID      = workflows.SummariseID
	SummaryWorkflowVersion = workflows.SummariseVersion
)

// SummaryPromptV0 and SummaryTemplateV0 hand out the two halves of that prompt,
// still carrying the {{TEMPLATE}} token: the splice happens in the caller.
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
