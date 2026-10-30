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

	"gocassini/internal/insight/workflows"
	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/transcribe"
)

// summaryBody checks that a written summary records the template that wrote
// it (D-819) and returns its text.
func summaryBody(t *testing.T, markdown []byte) string {
	t.Helper()
	ref, body, ok := transcribe.SummaryWorkflowOf(string(markdown))
	if !ok || ref.ID == "" || ref.Version == "" || ref.SHA256 == "" {
		t.Errorf("summary records no template: %q", markdown)
	}
	return body
}

// D-819 V3 end to end through `meetings summarize`: the configured custom
// template is the prompt sent, its id, version and hash are in the summary's
// front matter and in the sealed manifest's summary block, and the context a
// later insight reads carries the text without the block.
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
	ref, body, ok := transcribe.SummaryWorkflowOf(string(meeting.SummaryMarkdown))
	if !ok || ref != resumen.Ref() {
		t.Errorf("front matter = %+v (ok=%v), want %+v", ref, ok, resumen.Ref())
	}
	if body != returned+"\n" {
		t.Errorf("body = %q", body)
	}
	summary := decodePortableManifestFromOpus(t, path).Summary
	if summary["templateId"] != "resumen" || summary["templateVersion"] != "v3" || summary["templateSha256"] != resumen.SHA256 {
		t.Errorf("manifest summary = %v, want the custom template's id, version and hash", summary)
	}

	bundled := buildMeetingContext("mtg", meeting, meetingsCatalogEntry{})
	if strings.Contains(bundled.Summary.Markdown, "workflow:") || bundled.Summary.Markdown != returned+"\n" {
		t.Errorf("context summary = %q, want the text without front matter", bundled.Summary.Markdown)
	}
}

// A summary sealed before the template was recorded keeps the shipped version
// in the manifest: approximate, and the only thing there is to say.
func TestSummaryTemplateMetadataFallsBackForAnOlderSummary(t *testing.T) {
	meta := summaryTemplateMetadata([]byte("# Meeting Summary\n"))
	if meta["templateVersion"] != transcribe.SummaryWorkflowVersion || meta["templateId"] != nil {
		t.Errorf("meta = %v", meta)
	}
}
