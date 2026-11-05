package transcribe

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocassini/internal/insight"
	"gocassini/internal/insight/workflows"
	"gocassini/internal/portable"
)

// A summary switched off and a meeting nobody spoke in both leave no
// summary.md, and neither may be read as a failure.
func TestWriteSummaryArtifactSaysWhyItSkipped(t *testing.T) {
	prev := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(LLMConfig, insight.Workflow, []AudioStream, []Segment) (string, error) {
		t.Fatal("buildMeetingSummaryFn should not be called when the step is skipped")
		return "", nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	configured := LLMConfig{APIKey: "k", BaseURL: "https://example.test/api/v1", Model: "m"}
	off := configured
	off.Disabled = true
	for _, tc := range []struct {
		name     string
		llm      LLMConfig
		segments []Segment
		reason   string
	}{
		{"switched off", off, sampleSegments(), "disabled"},
		{"no endpoint", LLMConfig{}, sampleSegments(), "not_configured"},
		{"nobody spoke", configured, nil, "no_transcript"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			status, _, err := writeSummaryArtifact(tmp, sampleStreams(), tc.segments, BuildConfig{SummaryLLM: tc.llm}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if want := (portable.SummaryStatus{Status: "skipped", Reason: tc.reason}); status != want {
				t.Fatalf("status = %+v, want %+v", status, want)
			}
			if _, err := os.Stat(filepath.Join(tmp, "summary.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected no summary.md, stat err=%v", err)
			}
		})
	}
}

func TestWriteSummaryArtifactSkipsWhenLLMNotConfigured(t *testing.T) {
	tmp := t.TempDir()
	cfg := BuildConfig{} // SummaryLLM zero value, IsConfigured() == false

	prev := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(LLMConfig, insight.Workflow, []AudioStream, []Segment) (string, error) {
		t.Fatal("buildMeetingSummaryFn should not be called when SummaryLLM is unconfigured")
		return "", nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	var stdout bytes.Buffer
	status, _, err := writeSummaryArtifact(tmp, nil, sampleSegments(), cfg, &stdout)
	if err != nil {
		t.Fatalf("expected no error when summary disabled, got %v", err)
	}
	if want := (portable.SummaryStatus{Status: "skipped", Reason: "not_configured"}); status != want {
		t.Fatalf("status = %+v, want %+v", status, want)
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
	buildMeetingSummaryFn = func(LLMConfig, insight.Workflow, []AudioStream, []Segment) (string, error) {
		return "", errors.New("boom")
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	var stdout bytes.Buffer
	status, _, err := writeSummaryArtifact(tmp, sampleStreams(), sampleSegments(), cfg, &stdout)
	if err != nil {
		t.Fatalf("expected summary failure to be swallowed, got %v", err)
	}
	if want := (portable.SummaryStatus{Status: "failed", Reason: "llm_failed"}); untimed(t, status) != want {
		t.Fatalf("status = %+v, want %+v", status, want)
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
	var ran insight.WorkflowRef
	buildMeetingSummaryFn = func(_ LLMConfig, workflow insight.Workflow, _ []AudioStream, _ []Segment) (string, error) {
		ran = workflow.Ref()
		return expected, nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })
	t.Setenv(SummaryTemplateEnv, "")

	status, template, err := writeSummaryArtifact(tmp, sampleStreams(), sampleSegments(), cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if template.ID != "summarise" || template.SHA256 == "" || template != ran {
		t.Errorf("template = %+v, want the one the summary step ran (%+v)", template, ran)
	}
	if want := (portable.SummaryStatus{Status: "completed", Model: "test-model"}); untimed(t, status) != want {
		t.Fatalf("status = %+v, want %+v", status, want)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "summary.md"))
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if string(got) != expected {
		t.Fatalf("summary.md mismatch\nwant: %q\ngot:  %q", expected, string(got))
	}
}

// untimed checks that the outcome brackets the model call (D-899) and returns
// it without the times, which no test can predict.
func untimed(t *testing.T, status portable.SummaryStatus) portable.SummaryStatus {
	t.Helper()
	started, err := time.Parse(time.RFC3339Nano, status.StartedAt)
	if err != nil {
		t.Fatalf("startedAt = %q: %v", status.StartedAt, err)
	}
	finished, err := time.Parse(time.RFC3339Nano, status.FinishedAt)
	if err != nil {
		t.Fatalf("finishedAt = %q: %v", status.FinishedAt, err)
	}
	if finished.Before(started) {
		t.Fatalf("finishedAt %s is before startedAt %s", status.FinishedAt, status.StartedAt)
	}
	status.StartedAt, status.FinishedAt = "", ""
	return status
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
	// The prompt the step actually sends: the resolved workflow's, with no
	// template configured.
	t.Setenv(SummaryTemplateEnv, "")
	t.Setenv(workflows.TemplatesDirEnv, "")
	prompt := ResolveSummaryWorkflow().SystemPrompt()
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

func TestDefaultBuildConfigSummaryDisabledToggle(t *testing.T) {
	// Summarisation is configured from OPENROUTER_API_KEY alone;
	// CASSINI_SUMMARY_DISABLED must turn it off without unsetting the key.
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("CASSINI_SUMMARY_DISABLED", "1")

	cfg := DefaultBuildConfig()
	if cfg.SummaryLLM.IsConfigured() {
		t.Error("expected SummaryLLM to be unconfigured when CASSINI_SUMMARY_DISABLED=1")
	}
}

func TestDefaultBuildConfigSummaryEnabledByDefault(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("CASSINI_SUMMARY_DISABLED", "")

	cfg := DefaultBuildConfig()
	if !cfg.SummaryLLM.IsConfigured() {
		t.Error("expected SummaryLLM to be configured when CASSINI_SUMMARY_DISABLED is unset")
	}
}

func TestDefaultBuildConfigSummaryModelOverride(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "base-model")
	t.Setenv("SUMMARY_MODEL", "summary-model")

	cfg := DefaultBuildConfig()
	if cfg.SummaryLLM.Model != "summary-model" {
		t.Fatalf("expected SUMMARY_MODEL override to apply to summary, got %q", cfg.SummaryLLM.Model)
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

// A configured template that cannot run still gets the meeting a summary, by
// the built-in, and says so where a person reading the attempt looks: the
// step's log and its reason (D-819).
func TestWriteSummaryArtifactRecordsATemplateFallback(t *testing.T) {
	writeSummaryTemplates(t, map[string]string{"roto.md": "roto\n"})
	t.Setenv(SummaryTemplateEnv, "roto")
	warnings := useSummaryWarnings(t)
	prev := buildMeetingSummaryFn
	buildMeetingSummaryFn = func(_ LLMConfig, workflow insight.Workflow, _ []AudioStream, _ []Segment) (string, error) {
		return "# Summary\n", nil
	}
	t.Cleanup(func() { buildMeetingSummaryFn = prev })

	cfg := BuildConfig{SummaryLLM: LLMConfig{APIKey: "k", BaseURL: "https://example.test/api/v1", Model: "m"}}
	var stdout bytes.Buffer
	status, template, err := writeSummaryArtifact(t.TempDir(), sampleStreams(), sampleSegments(), cfg, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if want := (portable.SummaryStatus{Status: "completed", Reason: "template_fallback", Model: "m"}); untimed(t, status) != want {
		t.Errorf("status = %+v, want %+v", status, want)
	}
	if template.ID != "summarise" {
		t.Errorf("template = %+v, want the built-in", template)
	}
	if !strings.Contains(stdout.String(), `warn: summary template "roto" cannot run`) {
		t.Errorf("step log = %q, want the reason", stdout.String())
	}
	if warnings.Len() != 0 {
		t.Errorf("stderr warning %q, want it in the step log only", warnings)
	}
}

// The estimate reads the prompt of the template the step would run, not the
// built-in's (D-819).
func TestEstimateSummaryInputTokensUsesTheConfiguredTemplate(t *testing.T) {
	long := strings.Repeat("Resume en español con mucho detalle. ", 400)
	writeSummaryTemplates(t, map[string]string{
		"resumen.md": "---\nname: Resumen\nversion: 1\n---\n" + long + "\n",
	})
	t.Setenv(SummaryTemplateEnv, "")
	builtIn := EstimateSummaryInputTokens(sampleStreams(), sampleSegments())
	t.Setenv(SummaryTemplateEnv, "resumen")
	custom := EstimateSummaryInputTokens(sampleStreams(), sampleSegments())
	workflow, _ := resolveSummaryWorkflow()
	want := (len(workflow.SystemPrompt()) + len(formatTranscriptForSummary(sampleStreams(), sampleSegments())) + 3) / 4
	if workflow.ID != "resumen" || custom != want || custom == builtIn {
		t.Errorf("estimate = %d (built-in %d), want %d from %q's prompt", custom, builtIn, want, workflow.ID)
	}
}
