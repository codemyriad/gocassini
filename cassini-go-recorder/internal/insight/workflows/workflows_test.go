package workflows

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"gocassini/internal/insight"
)

// pinnedSHA256 is what each shipped workflow's bytes hash to today.
//
// This is the gate a prompt-only change has to pass, and it is why this
// package's tests run in lint.yml rather than only in ci.yml: ci.yml's
// paths-ignore skips '**/*.md', so editing a prompt runs no Go test at all
// there. A prompt is immutable — a change is a new version, a new pair of files
// and a new entry — so a hash that moves under an unchanged version is the
// failure this pins, not an inconvenience to be re-recorded.
//
// If you are here because this test failed: do not update the hash for the same
// version. Add the new pair as .v<N+1>.md, register it as a new entry, and pin
// its hash beside this one.
var pinnedSHA256 = map[string]string{
	SummariseID: "eba6bd6674d35522dddbd774d3fc6a1fbcdab025aa30a5993d49216d0c13f59f",
	TodosID:     "08b57fcab6894ab355d329c3a852bddda45e22ba11e93c1f3e0165c6d7c3c12f",
}

func TestShippedPromptBytesAreThePinnedOnes(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	ids := registry.IDs()
	if len(ids) != len(pinnedSHA256) {
		t.Fatalf("registry holds %v, but %d hashes are pinned; a workflow was added or removed without pinning it", ids, len(pinnedSHA256))
	}
	for _, id := range ids {
		workflow, ok := registry.Lookup(id)
		if !ok {
			t.Fatalf("registry lists %q and cannot look it up", id)
		}
		want, pinned := pinnedSHA256[id]
		if !pinned {
			t.Errorf("workflow %q ships with no pinned hash, so its prompt could be edited unnoticed", id)
			continue
		}
		if workflow.SHA256 != want {
			t.Errorf("workflow %q hashes to %s, pinned %s: the prompt bytes changed under an unchanged version %s", id, workflow.SHA256, want, workflow.Version)
		}
	}
}

// A prompt file nobody registered is a prompt nobody can run — and it would
// still be mirrored into skills/, licensed and shipped, which is how a
// half-landed workflow starts looking like a real one.
func TestEveryEmbeddedPromptIsClaimedByExactlyOneWorkflow(t *testing.T) {
	claimed := map[string]string{}
	for _, item := range shipped {
		for _, file := range []string{item.SystemFile, item.TemplateFile} {
			if owner, taken := claimed[file]; taken {
				t.Errorf("%s is claimed by both %q and %q; two workflows sharing bytes cannot be told apart by their hashes", file, owner, item.ID)
				continue
			}
			claimed[file] = item.ID
		}
	}

	embedded, err := fs.ReadDir(promptFS, promptDir)
	if err != nil {
		t.Fatalf("read %s: %v", promptDir, err)
	}
	for _, file := range embedded {
		if _, ok := claimed[file.Name()]; !ok {
			t.Errorf("%s/%s is embedded but no workflow references it", promptDir, file.Name())
		}
		delete(claimed, file.Name())
	}
	for file, owner := range claimed {
		t.Errorf("workflow %q names %s, which is not embedded", owner, file)
	}
}

// The panel and the runner must describe the same set. They are built from one
// table, so the thing worth asserting is that the instruction shown is the
// instruction sent — the whole reason the panel shows bytes rather than a
// paraphrase.
func TestCatalogShowsExactlyWhatTheRegistryWouldSend(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) != len(registry.IDs()) {
		t.Fatalf("catalog lists %d workflows, registry holds %d", len(entries), len(registry.IDs()))
	}
	for _, entry := range entries {
		workflow, ok := registry.Lookup(entry.ID)
		if !ok {
			t.Errorf("catalog lists %q, which the registry cannot run", entry.ID)
			continue
		}
		if entry.Instruction != workflow.SystemPrompt() {
			t.Errorf("workflow %q: the catalog's instruction is not the prompt the run would send", entry.ID)
		}
		if entry.SHA256 != workflow.SHA256 || entry.Version != workflow.Version {
			t.Errorf("workflow %q: catalog says %s/%s, registry says %s/%s", entry.ID, entry.Version, entry.SHA256, workflow.Version, workflow.SHA256)
		}
		if strings.Contains(entry.Instruction, insight.TemplatePlaceholder) {
			t.Errorf("workflow %q: the instruction still carries %s, so the panel would show a prompt nothing sends", entry.ID, insight.TemplatePlaceholder)
		}
		if entry.Origin != OriginBuiltIn {
			t.Errorf("workflow %q: origin = %q, want %q", entry.ID, entry.Origin, OriginBuiltIn)
		}
		// The name is what a person picks by and says nothing about what the
		// model is asked to do; the question is what the row discloses instead.
		// A row missing either is a row that cannot be chosen honestly.
		if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Question) == "" || strings.TrimSpace(entry.Description) == "" {
			t.Errorf("workflow %q: name=%q question=%q description=%q — the panel needs all three", entry.ID, entry.Name, entry.Question, entry.Description)
		}
	}
}

// The refusal D-718 is built on: a workflow whose bytes do not resolve is not
// offered at all. Registry fails rather than dropping it, because a set that
// silently shrank would be a settings panel quietly disagreeing with the
// binary behind it.
func TestAWorkflowWhoseBytesDoNotResolveFailsTheRegistry(t *testing.T) {
	missing := spec{
		ID:           "not-authored",
		Version:      "v0",
		SystemFile:   "not-authored.v0.md",
		TemplateFile: "not-authored-template.v0.md",
	}
	if _, err := missing.workflow(); err == nil {
		t.Fatal("a workflow naming a file that is not embedded was built anyway")
	} else if !strings.Contains(err.Error(), "not-authored") {
		t.Errorf("error = %v, want it to name the workflow that could not be resolved", err)
	}
}

// Every prompt pair uses the one splice grammar. A pair that did not would
// hash to bytes the runner never assembles.
func TestEveryPromptSplicesItsTemplate(t *testing.T) {
	for _, item := range shipped {
		system, err := readPrompt(item.SystemFile)
		if err != nil {
			t.Fatalf("workflow %q: %v", item.ID, err)
		}
		if !strings.Contains(system, insight.TemplatePlaceholder) {
			t.Errorf("workflow %q: %s has no %s, so its template would never be spliced in", item.ID, path.Join(promptDir, item.SystemFile), insight.TemplatePlaceholder)
		}
	}
}

// The pipeline's summary step reads its two halves through the accessors, which
// name their files in a //go:embed directive, while the registry names the same
// files in the table above. That is two declarations of one fact, so it is
// pinned: the day the summarise entry moves to a new pair, the accessors have
// to move with it or the pipeline goes on quietly sending the old prompt under
// the new workflow's hash.
func TestTheSummaryAccessorsHandOutTheRegistrysBytes(t *testing.T) {
	var summarise *spec
	for i := range shipped {
		if shipped[i].ID == SummariseID {
			summarise = &shipped[i]
		}
	}
	if summarise == nil {
		t.Fatalf("the registry no longer ships %q, which the pipeline's summary step runs", SummariseID)
	}

	system, err := readPrompt(summarise.SystemFile)
	if err != nil {
		t.Fatalf("read %s: %v", summarise.SystemFile, err)
	}
	template, err := readPrompt(summarise.TemplateFile)
	if err != nil {
		t.Fatalf("read %s: %v", summarise.TemplateFile, err)
	}
	if SummarisePromptV0() != system {
		t.Errorf("SummarisePromptV0 is not %s, the prompt the registry hashes for %q", summarise.SystemFile, SummariseID)
	}
	if SummariseTemplateV0() != template {
		t.Errorf("SummariseTemplateV0 is not %s, the template the registry hashes for %q", summarise.TemplateFile, SummariseID)
	}
}
