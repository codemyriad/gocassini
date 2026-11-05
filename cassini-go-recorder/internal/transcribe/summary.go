package transcribe

import (
	"context"
	"fmt"
	"strings"

	"gocassini/internal/insight"
	"gocassini/internal/insight/workflows"
	"gocassini/internal/portable"
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
// where the V0 structure template is spliced in. If the prompt file ever stops
// containing this token, the splice becomes a no-op and
// TestSummarySystemPromptPinsTemplateAndRules fails because the headings
// disappear from the prompt the step sends.
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

// WrittenSummary is a summary the model wrote, with what the caller records
// about it (D-819).
type WrittenSummary struct {
	// Body is the raw markdown for summary.md.
	Body string
	// Template is the workflow that wrote it, for the manifest.
	Template insight.WorkflowRef
	// Fallback says why the built-in wrote it in place of the configured
	// template, or "" when the configured one ran. A caller that records the
	// summary step's outcome records it as portable.SummaryReasonTemplateFallback.
	Fallback string
}

// Reason is the summary step's outcome reason for this summary: the
// template_fallback reason when the built-in stood in, else "".
func (w WrittenSummary) Reason() string {
	if w.Fallback != "" {
		return portable.SummaryReasonTemplateFallback
	}
	return ""
}

// BuildMeetingSummary calls the LLM to produce a meeting summary with the
// summary step's template. Speaker labels from streams are used so the model
// can attribute action items by name. A configured template that cannot run
// is warned about on summaryWarnings and reported in Fallback.
func BuildMeetingSummary(cfg LLMConfig, streams []AudioStream, segments []Segment) (WrittenSummary, error) {
	return writeSummaryWith(cfg, streams, segments)
}

// writeSummaryWith resolves the summary step's template, warns when that is
// the built-in standing in, and asks the model.
func writeSummaryWith(cfg LLMConfig, streams []AudioStream, segments []Segment) (WrittenSummary, error) {
	workflow, fallback := resolveSummaryWorkflow()
	if fallback != "" {
		fmt.Fprintf(summaryWarnings, "  warn: %s\n", fallback)
	}
	body, err := buildMeetingSummaryFn(cfg, workflow, streams, segments)
	if err != nil {
		return WrittenSummary{}, err
	}
	return WrittenSummary{Body: body, Template: workflow.Ref(), Fallback: fallback}, nil
}

// buildMeetingSummaryWith is BuildMeetingSummary with the template already
// resolved, so the pipeline can say in its own log when that is the
// built-in in place of the configured one.
func buildMeetingSummaryWith(cfg LLMConfig, workflow insight.Workflow, streams []AudioStream, segments []Segment) (string, error) {
	if !cfg.IsConfigured() {
		return "", fmt.Errorf("LLM not configured")
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("no transcript segments to summarize")
	}

	systemPrompt := workflow.SystemPrompt()
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

// EstimateSummaryInputTokens says roughly how many input tokens the summary
// step would send for this transcript: the system prompt of the template it
// would run (the configured one, or the built-in it falls back to) plus the
// transcript prose, at about four characters a token. It is for telling a person what a
// summary will cost before anything is sent, not for billing: providers count
// with their own tokenizers.
func EstimateSummaryInputTokens(streams []AudioStream, segments []Segment) int {
	workflow, _ := resolveSummaryWorkflow()
	chars := len(workflow.SystemPrompt()) + len(formatTranscriptForSummary(streams, segments))
	return (chars + 3) / 4
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
