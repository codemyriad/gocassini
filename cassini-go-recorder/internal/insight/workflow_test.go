package insight

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The hash is over the bytes that actually reach the model, not over the two
// halves separately: a change to how the splice works has to move it too.
func TestWorkflowHashesTheSplicedPrompt(t *testing.T) {
	workflow, err := NewWorkflow(WorkflowSpec{
		ID:       "summarise",
		Version:  "v0",
		System:   "Rules.\n\nTemplate:\n\n{{TEMPLATE}}\n",
		Template: "# Meeting Summary\n",
	})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}

	spliced := "Rules.\n\nTemplate:\n\n# Meeting Summary\n\n"
	if workflow.SystemPrompt() != spliced {
		t.Fatalf("spliced prompt = %q", workflow.SystemPrompt())
	}
	digest := sha256.Sum256([]byte(spliced))
	if workflow.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("sha256 = %q, want the digest of the spliced prompt", workflow.SHA256)
	}
	if workflow.Ref() != (WorkflowRef{ID: "summarise", Version: "v0", SHA256: workflow.SHA256}) {
		t.Errorf("ref = %+v", workflow.Ref())
	}
}

// The case the hash exists for: a prompt edited without a version bump.
func TestWorkflowHashMovesWhenEitherHalfChanges(t *testing.T) {
	base := WorkflowSpec{ID: "summarise", Version: "v0", System: "Rules.\n{{TEMPLATE}}", Template: "# A"}
	original, err := NewWorkflow(base)
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}

	editedSystem := base
	editedSystem.System = "Rules, revised.\n{{TEMPLATE}}"
	editedTemplate := base
	editedTemplate.Template = "# B"

	for name, spec := range map[string]WorkflowSpec{"system": editedSystem, "template": editedTemplate} {
		edited, err := NewWorkflow(spec)
		if err != nil {
			t.Fatalf("NewWorkflow(%s): %v", name, err)
		}
		if edited.SHA256 == original.SHA256 {
			t.Errorf("editing the %s left the hash unchanged", name)
		}
		if edited.Version != original.Version {
			t.Fatalf("this test only means something while the version is unchanged")
		}
	}
}

func TestNewWorkflowRefusesAMisassembledPrompt(t *testing.T) {
	cases := map[string]WorkflowSpec{
		"no id":                   {Version: "v0", System: "s"},
		"no version":              {ID: "summarise", System: "s"},
		"no system prompt":        {ID: "summarise", Version: "v0"},
		"a template with no slot": {ID: "summarise", Version: "v0", System: "Rules.", Template: "# A"},
		"a slot with no template": {ID: "summarise", Version: "v0", System: "Rules.\n{{TEMPLATE}}"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewWorkflow(spec); err == nil {
				t.Fatal("NewWorkflow accepted it")
			}
		})
	}
}

func TestWorkflowTakesQuestion(t *testing.T) {
	asks, err := NewWorkflow(WorkflowSpec{ID: "ask", Version: "v0", System: "Answer: {{QUESTION}}"})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}
	if !asks.TakesQuestion() {
		t.Error("a prompt with a question slot says it takes none")
	}
	summarise, err := NewWorkflow(WorkflowSpec{ID: "summarise", Version: "v0", System: "Summarise."})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}
	if summarise.TakesQuestion() {
		t.Error("a prompt with no question slot says it takes one")
	}
}

func TestRegistryRefusesAnAmbiguousSet(t *testing.T) {
	one, err := NewWorkflow(WorkflowSpec{ID: "summarise", Version: "v0", System: "a"})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}
	other, err := NewWorkflow(WorkflowSpec{ID: "summarise", Version: "v1", System: "b"})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}

	if _, err := NewRegistry(); err == nil {
		t.Error("an empty registry was accepted")
	}
	if _, err := NewRegistry(one, other); err == nil {
		t.Error("two workflows sharing an id were accepted")
	}
	if _, err := NewRegistry(Workflow{ID: "unhashed", Version: "v0", System: "a"}); err == nil {
		t.Error("a workflow with no content hash was accepted")
	}
}

func TestRegistryLookup(t *testing.T) {
	summarise, err := NewWorkflow(WorkflowSpec{ID: "summarise", Version: "v0", System: "a"})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}
	registry, err := NewRegistry(summarise)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got, ok := registry.Lookup(" summarise "); !ok || got.ID != "summarise" {
		t.Errorf("Lookup = %+v, %v", got, ok)
	}
	if _, ok := registry.Lookup("decisions"); ok {
		t.Error("Lookup found a workflow that does not exist")
	}
	if ids := strings.Join(registry.IDs(), ","); ids != "summarise" {
		t.Errorf("IDs = %q", ids)
	}
}
