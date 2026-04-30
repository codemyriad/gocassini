package transcribe

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed templates/summary.v0.md
var summaryV0Template string

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
	var sb strings.Builder
	sb.WriteString("You are a meeting-summary editor. Given a transcript of a meeting, produce a summary that follows the Markdown template below exactly.\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Preserve every heading verbatim, in the same order: \"# Meeting Summary\", \"## Overview\", \"## Key Points\", \"## Decisions\", \"## Action Items\", \"## Open Questions\", \"## Next Step\".\n")
	sb.WriteString("- Match each section's format: paragraph for Overview and Next Step; bullet list for Key Points, Decisions, and Open Questions; checkbox list for Action Items in the form \"- [ ] Owner - action item, due date if known\".\n")
	sb.WriteString("- Replace the placeholder text under each heading with content drawn from the transcript. Do not invent details that the transcript does not support.\n")
	sb.WriteString("- For Action Items, use the speaker's actual label as the owner when the transcript shows who committed to the item; use \"Unassigned\" otherwise.\n")
	sb.WriteString("- If a section has no relevant content, write \"None.\" on a single line under the heading. Do not omit the heading.\n")
	sb.WriteString("- Output ONLY the filled markdown. No preamble, no commentary, no code fences, no surrounding quotes.\n\n")
	sb.WriteString("Template:\n\n")
	sb.WriteString(template)
	return sb.String()
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
