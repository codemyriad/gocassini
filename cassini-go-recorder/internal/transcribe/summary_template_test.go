package transcribe

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/insight/workflows"
)

func useSummaryWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := summaryWarnings
	summaryWarnings = &buf
	t.Cleanup(func() { summaryWarnings = prev })
	return &buf
}

func writeSummaryTemplates(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(workflows.TemplatesDirEnv, dir)
}

func TestResolveSummaryWorkflow(t *testing.T) {
	writeSummaryTemplates(t, map[string]string{
		"resumen.md":  "---\nname: Resumen\nquestion: Resume.\nversion: 2\n---\nResume en español.\n",
		"pregunta.md": "---\nname: Pregunta\nquestion: \"\"\nversion: 1\n---\nResponde: {{QUESTION}}\n",
		"roto.md":     "roto\n",
	})
	cases := []struct {
		env, want, warn string
	}{
		{"", workflows.SummariseID, ""},
		{workflows.SummariseID, workflows.SummariseID, ""},
		{"resumen", "resumen", ""},
		{"roto", workflows.SummariseID, "cannot run"},
		{"missing", workflows.SummariseID, "no template has this id"},
		{"pregunta", workflows.SummariseID, "takes a question"},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			warnings := useSummaryWarnings(t)
			t.Setenv(SummaryTemplateEnv, tc.env)
			workflow := ResolveSummaryWorkflow()
			if workflow.ID != tc.want || workflow.SHA256 == "" {
				t.Errorf("resolved %q (%s), want %q", workflow.ID, workflow.SHA256, tc.want)
			}
			if tc.warn == "" && warnings.Len() != 0 {
				t.Errorf("unexpected warning: %s", warnings)
			}
			if tc.warn != "" && !strings.Contains(warnings.String(), tc.warn) {
				t.Errorf("warning = %q, want it to say %q", warnings, tc.warn)
			}
		})
	}
}

func TestSummaryFrontMatterRoundTrips(t *testing.T) {
	t.Setenv(SummaryTemplateEnv, "")
	t.Setenv(workflows.TemplatesDirEnv, "")
	workflow := ResolveSummaryWorkflow()
	markdown := summaryFrontMatter(workflow.Ref()) + "# Meeting Summary\n"
	ref, body, ok := SummaryWorkflowOf(markdown)
	if !ok || ref != workflow.Ref() || body != "# Meeting Summary\n" {
		t.Errorf("read back %+v %q ok=%v", ref, body, ok)
	}
	for _, plain := range []string{"# Meeting Summary\n", "---\ntitle: x\n---\nbody", "---\nworkflow:\n  id: \"x\"\nunclosed"} {
		if _, body, ok := SummaryWorkflowOf(plain); ok || body != plain {
			t.Errorf("%q read as front matter", plain)
		}
	}
}

// The summary the pipeline writes names the template that wrote it.
func TestBuildMeetingSummaryRecordsItsTemplate(t *testing.T) {
	writeSummaryTemplates(t, map[string]string{
		"resumen.md": "---\nname: Resumen\nquestion: Resume.\nversion: 2\n---\nResume en español.\n",
	})
	t.Setenv(SummaryTemplateEnv, "resumen")
	srv := newChatStub(t, "# Resumen\n\nHola.")
	body, err := BuildMeetingSummary(LLMConfig{BaseURL: srv, Model: "m"}, sampleStreams(), sampleSegments())
	if err != nil {
		t.Fatal(err)
	}
	ref, text, ok := SummaryWorkflowOf(body)
	if !ok || ref.ID != "resumen" || ref.Version != "v2" || text != "# Resumen\n\nHola.\n" {
		t.Errorf("summary = %q", body)
	}
}

func newChatStub(t *testing.T, reply string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}, "finish_reason": "stop"}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
