package transcribe

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSummaryArtifactSkipsWhenLLMNotConfigured(t *testing.T) {
	tmp := t.TempDir()
	cfg := BuildConfig{} // SummaryLLM zero value, IsConfigured() == false

	prev := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(LLMConfig, []AudioStream, []Segment) (string, error) {
		t.Fatal("buildMeetingSummaryFn should not be called when SummaryLLM is unconfigured")
		return "", nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	var stdout bytes.Buffer
	if err := writeSummaryArtifact(tmp, nil, sampleSegments(), cfg, &stdout); err != nil {
		t.Fatalf("expected no error when summary disabled, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "summary.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no summary.md when summary disabled, stat err=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected silent skip when summary disabled, got stdout=%q", stdout.String())
	}
}

func TestWriteSummaryArtifactWarnsAndSkipsOnError(t *testing.T) {
	tmp := t.TempDir()
	cfg := BuildConfig{
		SummaryLLM: LLMConfig{
			APIKey:  "test-key",
			BaseURL: "https://example.test/api/v1",
			Model:   "test-model",
		},
	}

	prev := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(LLMConfig, []AudioStream, []Segment) (string, error) {
		return "", errors.New("boom")
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	var stdout bytes.Buffer
	if err := writeSummaryArtifact(tmp, sampleStreams(), sampleSegments(), cfg, &stdout); err != nil {
		t.Fatalf("expected summary failure to be swallowed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "summary.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no summary.md after failure, stat err=%v", err)
	}
	if !strings.Contains(stdout.String(), "warn: summary generation failed: boom") {
		t.Fatalf("expected warning in stdout, got %q", stdout.String())
	}
}

func TestWriteSummaryArtifactWritesSummaryMd(t *testing.T) {
	tmp := t.TempDir()
	cfg := BuildConfig{
		SummaryLLM: LLMConfig{
			APIKey:  "test-key",
			BaseURL: "https://example.test/api/v1",
			Model:   "test-model",
		},
	}

	const expected = "# Meeting Summary\n\n## Overview\n\nDemo overview.\n"
	prev := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(LLMConfig, []AudioStream, []Segment) (string, error) {
		return expected, nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	if err := writeSummaryArtifact(tmp, sampleStreams(), sampleSegments(), cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "summary.md"))
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if string(got) != expected {
		t.Fatalf("summary.md mismatch\nwant: %q\ngot:  %q", expected, string(got))
	}
}

func TestBuildMeetingSummaryRejectsUnconfiguredLLM(t *testing.T) {
	_, err := BuildMeetingSummary(LLMConfig{}, sampleStreams(), sampleSegments())
	if err == nil || !strings.Contains(err.Error(), "LLM not configured") {
		t.Fatalf("expected LLM-not-configured error, got %v", err)
	}
}

func TestBuildMeetingSummaryRejectsEmptyTranscript(t *testing.T) {
	cfg := LLMConfig{APIKey: "test", BaseURL: "https://example.test/api/v1", Model: "m"}
	_, err := BuildMeetingSummary(cfg, sampleStreams(), nil)
	if err == nil || !strings.Contains(err.Error(), "no transcript segments") {
		t.Fatalf("expected empty-transcript error, got %v", err)
	}
}

func TestSummarySystemPromptPinsTemplateAndRules(t *testing.T) {
	prompt := summarySystemPrompt(summaryV0Template)
	mustContain := []string{
		"# Meeting Summary",
		"## Overview",
		"## Key Points",
		"## Decisions",
		"## Action Items",
		"## Open Questions",
		"## Next Step",
		"Output ONLY the filled markdown",
		"\"None.\"",
	}
	for _, s := range mustContain {
		if !strings.Contains(prompt, s) {
			t.Errorf("system prompt missing %q", s)
		}
	}
}

func TestFormatTranscriptForSummaryUsesLabels(t *testing.T) {
	streams := []AudioStream{
		{SpeakerID: "spk_alex", SpeakerLabel: "Alex"},
		{SpeakerID: "spk_chris", SpeakerLabel: "Chris"},
	}
	segments := []Segment{
		{SpeakerID: "spk_alex", Text: "hello there"},
		{SpeakerID: "spk_chris", Text: "hi back"},
		{SpeakerID: "spk_alex", Text: "  "}, // whitespace-only segment is dropped
	}
	got := formatTranscriptForSummary(streams, segments)
	want := "Alex: hello there\nChris: hi back\n"
	if got != want {
		t.Fatalf("transcript formatting mismatch\nwant: %q\ngot:  %q", want, got)
	}
}

func TestStripMarkdownFencesUnwrapsOuterFence(t *testing.T) {
	cases := map[string]string{
		"```markdown\n# Hi\n```":   "# Hi",
		"```\n# Hi\n```":           "# Hi",
		"# Hi":                     "# Hi",
		"```md\n# Outer\n```\n```": "# Outer\n```",
	}
	for in, want := range cases {
		got := stripMarkdownFences(in)
		if got != want {
			t.Errorf("stripMarkdownFences(%q) = %q, want %q", in, got, want)
		}
	}
}

func sampleStreams() []AudioStream {
	return []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
}

func sampleSegments() []Segment {
	return []Segment{{
		SpeakerID: "spk_alex",
		StartMS:   0,
		EndMS:     1000,
		Text:      "hello world",
		Words: []Word{
			{Text: "hello", StartMS: 0, EndMS: 500},
			{Text: "world", StartMS: 500, EndMS: 1000},
		},
	}}
}
