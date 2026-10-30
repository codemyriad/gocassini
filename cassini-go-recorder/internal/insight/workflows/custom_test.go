package workflows

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/insight"
)

const goodTemplate = `---
name: "Resumen de la reunión"
description: Resumen, decisiones y próximos pasos.
version: 3
---

Eres un asistente que resume reuniones. Escribe todo en español.

## Resumen
`

// askTemplate takes the caller's question the way the built-in ask does: the
// {{QUESTION}} slot in its bytes is the whole of it.
const askTemplate = `---
name: 'Pregunta libre'
description: Responde a tu pregunta.
version: 1
---
Responde a esta pregunta: {{QUESTION}}
`

func writeTemplateFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// The V1 demo: a good file, a broken file and a file shadowing a built-in sit
// in the directory; the good one runs, the other two are listed with a reason
// and stay out of the registry, and the built-ins are untouched.
func TestLoadDirReadsCustomTemplatesBesideTheBuiltIns(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, "resumen.md", goodTemplate)
	writeTemplateFile(t, dir, "pregunta.md", askTemplate)
	writeTemplateFile(t, dir, "broken.md", "no front matter here\n")
	writeTemplateFile(t, dir, SummariseID+".md", goodTemplate)
	writeTemplateFile(t, dir, ".resumen.md.tmp", "ignored")
	writeTemplateFile(t, dir, "notes.txt", "ignored")
	if err := os.Mkdir(filepath.Join(dir, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	set, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	gotIDs := []string{}
	for _, entry := range set.Entries {
		gotIDs = append(gotIDs, entry.Origin+":"+entry.ID)
	}
	wantIDs := []string{
		OriginBuiltIn + ":" + SummariseID, OriginBuiltIn + ":" + TodosID, OriginBuiltIn + ":" + AskID,
		OriginCustom + ":broken", OriginCustom + ":pregunta", OriginCustom + ":resumen", OriginCustom + ":" + SummariseID,
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("entries = %v, want %v", gotIDs, wantIDs)
	}

	resumen, ok := set.Registry.Lookup("resumen")
	if !ok {
		t.Fatalf("the good file is not in the registry: %v", set.Registry.IDs())
	}
	if resumen.Version != "v3" {
		t.Errorf("version = %q, want v3", resumen.Version)
	}
	wantInstruction := "Eres un asistente que resume reuniones. Escribe todo en español.\n\n## Resumen"
	if resumen.SystemPrompt() != wantInstruction {
		t.Errorf("instruction = %q, want %q", resumen.SystemPrompt(), wantInstruction)
	}
	if resumen.TakesQuestion() {
		t.Error("a template with no {{QUESTION}} was read as taking the caller's question")
	}

	pregunta, ok := set.Registry.Lookup("pregunta")
	if !ok || !pregunta.TakesQuestion() {
		t.Errorf("the {{QUESTION}} template is missing or does not take a question: %v", ok)
	}

	builtIn, ok := set.Registry.Lookup(SummariseID)
	if !ok || builtIn.Version != SummariseVersion || builtIn.SHA256 != pinnedSHA256[SummariseID] {
		t.Errorf("the shadowing file replaced or disturbed the built-in %q: %+v", SummariseID, builtIn)
	}
	if _, ok := set.Registry.Lookup("broken"); ok {
		t.Error("a broken file is in the registry")
	}

	for _, entry := range set.Entries {
		switch {
		case entry.Origin == OriginCustom && entry.ID == "resumen":
			if entry.Error != "" || entry.SHA256 != resumen.SHA256 || entry.Name != "Resumen de la reunión" ||
				entry.Description != "Resumen, decisiones y próximos pasos." {
				t.Errorf("resumen entry = %+v", entry)
			}
			if entry.Instruction != resumen.SystemPrompt() {
				t.Error("the catalogue's instruction is not what the run would send")
			}
		case entry.Origin == OriginCustom && entry.ID == "broken":
			if entry.Error == "" || entry.SHA256 != "" || !strings.Contains(entry.Error, "front matter") {
				t.Errorf("broken entry = %+v, want an error about its front matter and no hash", entry)
			}
			if set.Problem("broken") != entry.Error {
				t.Errorf("Problem(broken) = %q", set.Problem("broken"))
			}
		case entry.Origin == OriginCustom && entry.ID == SummariseID:
			if !strings.Contains(entry.Error, "built-in") || entry.SHA256 != "" {
				t.Errorf("shadowing entry = %+v, want a built-in clash error and no hash", entry)
			}
		case entry.Origin == OriginBuiltIn:
			if entry.Error != "" {
				t.Errorf("built-in %q carries an error: %s", entry.ID, entry.Error)
			}
		}
	}
	if set.Problem("resumen") != "" || set.Problem("nothing") != "" {
		t.Error("Problem reported a template that is fine or absent")
	}
}

func TestLoadDirWithNoDirectoryIsBuiltInsOnly(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		set, err := LoadDir(dir)
		if err != nil {
			t.Fatalf("LoadDir(%q): %v", dir, err)
		}
		if len(set.Entries) != len(shipped) || len(set.Registry.IDs()) != len(shipped) {
			t.Errorf("LoadDir(%q) = %d entries, want the %d built-ins", dir, len(set.Entries), len(shipped))
		}
	}
}

func TestRegistryReadsTheDirectoryTheEnvironmentNames(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, "resumen.md", goodTemplate)
	t.Setenv(TemplatesDirEnv, dir)
	registry, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	if _, ok := registry.Lookup("resumen"); !ok {
		t.Errorf("Registry ignored %s: %v", TemplatesDirEnv, registry.IDs())
	}
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if last := entries[len(entries)-1]; last.ID != "resumen" || last.Origin != OriginCustom {
		t.Errorf("Catalog's last entry = %+v, want the custom one", last)
	}
}

// The hash is the record's claim about the bytes, so it must be the plain
// SHA-256 of the instruction, the same as a built-in's is of its spliced prompt.
func TestACustomTemplateHashesItsInstruction(t *testing.T) {
	tpl, err := ParseCustomTemplate("resumen", []byte(goodTemplate))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	workflow, err := tpl.Workflow()
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	direct, err := insight.NewWorkflow(insight.WorkflowSpec{ID: "resumen", Version: "v3", System: tpl.Instruction})
	if err != nil {
		t.Fatal(err)
	}
	if workflow.SHA256 != direct.SHA256 {
		t.Errorf("hash = %s, want %s", workflow.SHA256, direct.SHA256)
	}
	// CRLF line endings are the same template.
	crlf, err := ParseCustomTemplate("resumen", []byte(strings.ReplaceAll(goodTemplate, "\n", "\r\n")))
	if err != nil {
		t.Fatalf("parse CRLF: %v", err)
	}
	if crlf != tpl {
		t.Errorf("CRLF parse = %+v, want %+v", crlf, tpl)
	}
}

func TestParseCustomTemplateRefusesWhatWillNotRun(t *testing.T) {
	front := func(lines ...string) string {
		return "---\n" + strings.Join(lines, "\n") + "\n---\n"
	}
	cases := []struct {
		name, id, file, want string
	}{
		{"bad id", "has space", goodTemplate, "not a usable id"},
		{"no front matter", "x", "Just an instruction\n", "must start with"},
		{"unclosed front matter", "x", "---\nname: X\nversion: 1\nbody", "no closing"},
		{"no name", "x", front("version: 1") + "Body", "needs a name"},
		{"no version", "x", front("name: X") + "Body", "needs a version"},
		{"bad version", "x", front("name: X", "version: two") + "Body", "whole number"},
		{"zero version", "x", front("name: X", "version: 0") + "Body", "whole number"},
		{"unknown field", "x", front("name: X", "version: 1", "langauge: es") + "Body", "unknown field"},
		{"duplicate field", "x", front("name: X", "name: Y", "version: 1") + "Body", "twice"},
		{"multi-line value", "x", front("name: |", "version: 1") + "Body", "multi-line"},
		{"unclosed quote", "x", front(`name: "X`, "version: 1") + "Body", "does not close"},
		{"not key value", "x", front("name X", "version: 1") + "Body", "not 'key: value'"},
		{"empty instruction", "x", front("name: X", "version: 1") + "\n\n", "empty"},
		{"template slot", "x", front("name: X", "version: 1") + "Body {{TEMPLATE}}", "has no place for a template splice"},
		{"question field", "x", front("name: X", "version: 1", "question: Q") + "Body", "no longer part of a template"},
		{"empty question field", "x", front("name: X", "version: 1", `question: ""`) + "Body {{QUESTION}}", "no longer part of a template"},
		{"not utf-8", "x", "---\nname: \xff\nversion: 1\n---\nBody", "UTF-8"},
		{"too large", "x", front("name: X", "version: 1") + strings.Repeat("a", maxCustomTemplateBytes), "at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCustomTemplate(tc.id, []byte(tc.file))
			if err == nil {
				t.Fatalf("parsed; want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseCustomTemplateReadsTheScalarForms(t *testing.T) {
	file := "---\n# a comment\nname: 'It''s ours'\ndescription: \"Tab\\there, «quoted»\"\nversion: 12 # trailing comment\n---\nBody\n"
	tpl, err := ParseCustomTemplate("ours", []byte(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tpl.Name != "It's ours" || tpl.Description != "Tab\there, «quoted»" || tpl.Version != 12 || tpl.Instruction != "Body" {
		t.Errorf("parsed %+v", tpl)
	}
}

// The operator writes this exact shape from the panel (cassini-operator
// settings_workflows_write.go, pinned there on the rendered bytes); it must
// read back to the same values, Go-quoted escapes included.
func TestParseCustomTemplateReadsWhatTheOperatorWrites(t *testing.T) {
	file := "---\nname: \"Say \\\"hi\\\" — ¡ya!\"\ndescription: \"a\\\\b\"\nversion: 4\n---\n\nAnswer {{QUESTION}}\n"
	tpl, err := ParseCustomTemplate("hola", []byte(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tpl.Name != `Say "hi" — ¡ya!` || tpl.Description != `a\b` || tpl.Version != 4 || tpl.Instruction != "Answer {{QUESTION}}" {
		t.Errorf("parsed %+v", tpl)
	}

	// The description is optional: the operator writes an empty one as "".
	empty, err := ParseCustomTemplate("hola", []byte("---\nname: \"X\"\ndescription: \"\"\nversion: 1\n---\n\nBody\n"))
	if err != nil || empty.Description != "" {
		t.Errorf("empty description: %+v, %v", empty, err)
	}
}
