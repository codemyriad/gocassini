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

	"gocassini/internal/insight"
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
		"resumen.md":  "---\nname: Resumen\nversion: 2\n---\nResume en español.\n",
		"pregunta.md": "---\nname: Pregunta\nversion: 1\n---\nResponde: {{QUESTION}}\n",
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
		{workflows.AskID, workflows.SummariseID, "takes a question"},
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

func TestSummaryTemplateRecordOf(t *testing.T) {
	if SummaryTemplateRecordOf(insight.WorkflowRef{}) != nil {
		t.Error("an empty ref became a record")
	}
	got := SummaryTemplateRecordOf(insight.WorkflowRef{ID: "resumen", Version: "v2", SHA256: "abc"})
	if got == nil || *got != (SummaryTemplateRecord{TemplateID: "resumen", TemplateVersion: "v2", TemplateSHA256: "abc"}) {
		t.Errorf("record = %+v", got)
	}
}

// The summary is the document and nothing else; the template that wrote it is
// returned for the manifest (D-819).
func TestBuildMeetingSummaryReturnsItsTemplateAndWritesOnlyTheText(t *testing.T) {
	writeSummaryTemplates(t, map[string]string{
		"resumen.md": "---\nname: Resumen\nversion: 2\n---\nResume en español.\n",
	})
	t.Setenv(SummaryTemplateEnv, "resumen")
	srv := newChatStub(t, "# Resumen\n\nHola.")
	body, ref, err := BuildMeetingSummary(LLMConfig{BaseURL: srv, Model: "m"}, sampleStreams(), sampleSegments())
	if err != nil {
		t.Fatal(err)
	}
	if body != "# Resumen\n\nHola.\n" {
		t.Errorf("summary = %q, want the model's text alone", body)
	}
	if ref.ID != "resumen" || ref.Version != "v2" || ref.SHA256 == "" {
		t.Errorf("template = %+v", ref)
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

// An unreadable templates directory is a fallback only for a configured custom
// template, and the warning names the directory's error rather than "no
// template has this id". The built-in, configured or by default, runs without
// a warning: it is what was asked for.
func TestResolveSummaryWorkflowWithAnUnreadableDirectory(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "templates")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(workflows.TemplatesDirEnv, notADir)
	for _, tc := range []struct{ env, warn string }{
		{"", ""},
		{workflows.SummariseID, ""},
		{"resumen", "could not be read"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(SummaryTemplateEnv, tc.env)
			workflow, fallback := resolveSummaryWorkflow()
			if workflow.ID != workflows.SummariseID {
				t.Errorf("resolved %q", workflow.ID)
			}
			if tc.warn == "" && fallback != "" {
				t.Errorf("fallback = %q, want none", fallback)
			}
			if tc.warn != "" && !strings.Contains(fallback, tc.warn) {
				t.Errorf("fallback = %q, want it to say %q", fallback, tc.warn)
			}
		})
	}
}
