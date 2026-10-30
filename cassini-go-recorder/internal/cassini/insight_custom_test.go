package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/insight"
	"gocassini/internal/insight/workflows"
)

const customResumen = `---
name: Resumen
description: Un resumen en español.
version: 2
---
Resume la reunión en español.
`

// D-819 V1 from the command line: a file in the templates directory is listed
// as Custom, runs by its id and is recorded by its own version and hash; a file
// that cannot run is listed with its reason and refused as a bad request.
func TestInsightRunsACustomTemplateFromTheTemplatesDirectory(t *testing.T) {
	dir := t.TempDir()
	templates := filepath.Join(dir, "templates")
	if err := os.Mkdir(templates, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templates, "resumen.md"), []byte(customResumen), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templates, "roto.md"), []byte("---\nname: Roto\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The question field is gone from the format; a file still carrying one is
	// refused rather than read as something it no longer means.
	if err := os.WriteFile(filepath.Join(templates, "preguntas.md"), []byte("---\nname: Preguntas\nquestion: ¿Qué se decidió?\nversion: 1\n---\nResponde.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(workflows.TemplatesDirEnv, templates)

	var listing, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"insight", "workflows", "--json"}, &listing, &stderr); code != 0 {
		t.Fatalf("workflows exit = %d\nstderr: %s", code, stderr.String())
	}
	var entries []workflows.Entry
	if err := json.Unmarshal(listing.Bytes(), &entries); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	byID := map[string]workflows.Entry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	if got := byID["resumen"]; got.Origin != workflows.OriginCustom || got.Error != "" || got.SHA256 == "" {
		t.Errorf("resumen listed as %+v", got)
	}
	if got := byID["roto"]; got.Origin != workflows.OriginCustom || got.Error == "" || got.SHA256 != "" {
		t.Errorf("roto listed as %+v, want it broken with a reason", got)
	}

	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Lunes", "", ""))
	summaryLLMEnv(t, "http://model.invalid/v1")

	for _, id := range []string{"roto", "preguntas"} {
		if id != "roto" && !strings.Contains(byID[id].Error, "question field is no longer part") {
			t.Errorf("%s listed as %+v, want it broken because it takes a question", id, byID[id])
		}
		stderr.Reset()
		code := Run(context.Background(), []string{"insight", "run", "--context", bundle, "--workflow", id}, &bytes.Buffer{}, &stderr)
		if code != 2 {
			t.Fatalf("broken template %s exit = %d, want 2\nstderr: %s", id, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), byID[id].Error) {
			t.Errorf("the refusal of %s does not give the listed reason: %s", id, stderr.String())
		}
	}

	provider := &stubInsightProvider{reply: "# Resumen\n\nNada.\n"}
	useInsightProvider(t, provider)
	recordPath := filepath.Join(dir, "insight.json")
	stderr.Reset()
	code := Run(context.Background(), []string{"insight", "run", "--context", bundle, "--workflow", "resumen", "--out", filepath.Join(dir, "R.md"), "--record", recordPath}, &bytes.Buffer{}, &stderr)
	if code != 0 {
		t.Fatalf("custom run exit = %d\nstderr: %s", code, stderr.String())
	}
	if provider.system != "Resume la reunión en español." {
		t.Errorf("system prompt sent = %q", provider.system)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record insight.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Workflow.ID != "resumen" || record.Workflow.Version != "v2" || record.Workflow.SHA256 != byID["resumen"].SHA256 {
		t.Errorf("record names %+v, want resumen/v2/%s", record.Workflow, byID["resumen"].SHA256)
	}
}
