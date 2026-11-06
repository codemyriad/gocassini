package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gocassini/internal/insight"
	"gocassini/internal/insight/workflows"
	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/transcribe"
)

// summaryBody checks that a written summary is the document alone — the
// template that wrote it lives in the manifest, never in summary.md (D-819).
func summaryBody(t *testing.T, markdown []byte) string {
	t.Helper()
	if strings.HasPrefix(string(markdown), "---") {
		t.Errorf("summary carries front matter: %q", markdown)
	}
	return string(markdown)
}

// shippedSummariseRef is the built-in summary template, which a run with no
// template configured records.
func shippedSummariseRef(t *testing.T) insight.WorkflowRef {
	t.Helper()
	set, err := workflows.LoadDir("")
	if err != nil {
		t.Fatal(err)
	}
	workflow, ok := set.Registry.Lookup(workflows.SummariseID)
	if !ok {
		t.Fatal("no built-in summarise")
	}
	return workflow.Ref()
}

// D-819 V3 end to end through `meetings summarize` on a meeting file: the
// configured custom template is the prompt sent, summary.md is the model's
// text alone, and the sealed manifest's summary block names the template's
// id, version and hash.
func TestMeetingsSummarizeRunsTheConfiguredCustomTemplate(t *testing.T) {
	requireFFMediaTools(t)
	templates := t.TempDir()
	custom := "---\nname: Resumen\ndescription: \"\"\nversion: 3\n---\n\nResume la reunión en español.\n"
	if err := os.WriteFile(filepath.Join(templates, "resumen.md"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(workflows.TemplatesDirEnv, templates)
	t.Setenv(transcribe.SummaryTemplateEnv, "resumen")
	set, err := workflows.Load()
	if err != nil {
		t.Fatal(err)
	}
	resumen, ok := set.Registry.Lookup("resumen")
	if !ok {
		t.Fatalf("the custom template did not load: %v", set.Entries)
	}

	const returned = "# Resumen\n\nTodo en español."
	var mu sync.Mutex
	var system string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		for _, message := range request.Messages {
			if message.Role == "system" {
				system = message.Content
			}
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": returned}}},
		})
	}))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	path := packFixtureOpus(t, t.TempDir(), "meeting")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"meetings", "summarize", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("summarize exit = %d\nstderr: %s", code, stderr.String())
	}
	mu.Lock()
	sent := system
	mu.Unlock()
	if sent != resumen.SystemPrompt() {
		t.Errorf("system prompt = %q, want the custom template's", sent)
	}

	meeting, err := inspectpkg.ExtractMeeting(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(meeting.SummaryMarkdown) != returned+"\n" {
		t.Errorf("summary.md = %q, want the model's text alone", meeting.SummaryMarkdown)
	}
	summary := decodePortableManifestFromOpus(t, path).Summary
	if summary["templateId"] != "resumen" || summary["templateVersion"] != "v3" || summary["templateSha256"] != resumen.SHA256 {
		t.Errorf("manifest summary = %v, want the custom template's id, version and hash", summary)
	}
}

// A bundle built before the summary step recorded its template packs with the
// shipped version and no id or hash: approximate, and the only thing there is
// to say.
func TestSummaryTemplateMetadataFallsBackForAnOlderBundle(t *testing.T) {
	meta := summaryTemplateMetadata(nil)
	if meta["templateVersion"] != transcribe.SummaryWorkflowVersion || meta["templateId"] != nil || meta["templateSha256"] != nil {
		t.Errorf("meta = %v", meta)
	}
	meta = summaryTemplateMetadata(&transcribe.SummaryTemplateRecord{TemplateID: "resumen", TemplateVersion: "v3", TemplateSHA256: "abc"})
	if meta["templateId"] != "resumen" || meta["templateVersion"] != "v3" || meta["templateSha256"] != "abc" {
		t.Errorf("meta = %v", meta)
	}
}
