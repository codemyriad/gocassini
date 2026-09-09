package insight

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// TemplatePlaceholder is the token a workflow's system prompt carries where
// its output template is spliced in. It is the token every shipped prompt
// already uses (internal/insight/workflows/prompts/), because a second splice
// grammar would mean the same prompt bytes render differently depending on
// which caller sent them.
const TemplatePlaceholder = "{{TEMPLATE}}"

// QuestionPlaceholder is the token a freeform workflow carries where the
// caller's question is spliced in.
//
// A workflow either takes a question or it does not, and Run refuses the
// mismatch in both directions: a question handed to a workflow with no slot
// would be silently dropped, and an empty slot would ask the model to answer a
// question that is not there. No shipped workflow carries this token yet: the
// prototype's "Ask your own question" is the caller's own text at run time and
// has no authored prompt behind it (D-718). The record has a place for the
// question, so the mechanism that fills it exists rather than being implied.
const QuestionPlaceholder = "{{QUESTION}}"

// Workflow is one prompt this product will run, identified well enough that a
// document produced by it can be traced back to the exact bytes that produced
// it.
//
// Which prompts exist is internal/insight/workflows' business, not this
// package's. What is fixed here is the identity every one of them carries, and
// in particular the field that costs something to add later is SHA256: an artifact
// written without it can never be told apart from an artifact written by a
// later edit of the same named version.
//
// Version and SHA256 are not redundant. Version is the claim a human makes
// about the prompt; SHA256 is what is actually true about the bytes. A prompt
// edited without a version bump is exactly the case worth catching.
type Workflow struct {
	// ID is the stable name a caller asks for, e.g. "summarise".
	ID string
	// Version is the immutable version of the prompt, e.g. "v0". A prompt is
	// never edited in place: a change is a new version.
	Version string
	// System is the system prompt, still carrying TemplatePlaceholder (and
	// QuestionPlaceholder for a workflow that takes a question).
	System string
	// Template is the output skeleton spliced into System.
	Template string
	// SHA256 is the hex digest of the spliced system prompt — System with
	// Template resolved — which is the byte string that actually reaches the
	// model. Hashing the halves separately would let a change in the splice
	// grammar pass unnoticed.
	SHA256 string
}

// WorkflowSpec is what a caller has before a workflow is validated and hashed.
//
// It exists so that the prompt bytes stay where they are compiled in. Go's
// embed cannot escape its own package directory, so a package that embedded
// prompts here would be a second copy of bytes that live somewhere already —
// and two copies of a prompt is the drift the content hash exists to detect,
// installed on purpose. internal/insight/workflows owns the files and passes
// them in.
type WorkflowSpec struct {
	ID       string
	Version  string
	System   string
	Template string
}

// NewWorkflow validates a spec and computes its content hash.
//
// The template splice is checked in both directions: a template with nowhere
// to go, and a placeholder with nothing to put in it, are both prompts that
// reach the model as something other than what their author wrote.
func NewWorkflow(spec WorkflowSpec) (Workflow, error) {
	id := strings.TrimSpace(spec.ID)
	version := strings.TrimSpace(spec.Version)
	switch {
	case id == "":
		return Workflow{}, fmt.Errorf("a workflow needs an id")
	case version == "":
		return Workflow{}, fmt.Errorf("workflow %q needs a version", id)
	case strings.TrimSpace(spec.System) == "":
		return Workflow{}, fmt.Errorf("workflow %q has an empty system prompt", id)
	}

	hasSlot := strings.Contains(spec.System, TemplatePlaceholder)
	hasTemplate := strings.TrimSpace(spec.Template) != ""
	switch {
	case hasTemplate && !hasSlot:
		return Workflow{}, fmt.Errorf("workflow %q carries a template but its system prompt has no %s to splice it into", id, TemplatePlaceholder)
	case hasSlot && !hasTemplate:
		return Workflow{}, fmt.Errorf("workflow %q has a %s slot but no template to splice into it", id, TemplatePlaceholder)
	}

	workflow := Workflow{
		ID:       id,
		Version:  version,
		System:   spec.System,
		Template: spec.Template,
	}
	digest := sha256.Sum256([]byte(workflow.SystemPrompt()))
	workflow.SHA256 = hex.EncodeToString(digest[:])
	return workflow, nil
}

// SystemPrompt resolves the template into the system prompt. It is what the
// hash is taken over and what Run sends, so the two can never disagree — which
// is also why the settings panel shows this and not a description of it
// (D-718): the bytes on the screen are the bytes SHA256 names.
func (w Workflow) SystemPrompt() string {
	if w.Template == "" {
		return w.System
	}
	return strings.Replace(w.System, TemplatePlaceholder, w.Template, 1)
}

// TakesQuestion reports whether this workflow expects a freeform question.
func (w Workflow) TakesQuestion() bool {
	return strings.Contains(w.System, QuestionPlaceholder)
}

// Ref is the workflow's identity as it is written into an artifact record.
func (w Workflow) Ref() WorkflowRef {
	return WorkflowRef{ID: w.ID, Version: w.Version, SHA256: w.SHA256}
}

// Registry is the set of workflows a run may name.
//
// It is a lookup and nothing else on purpose: which workflows exist, and where
// their bytes come from, belongs to internal/insight/workflows, which owns the
// files and serves them. What belongs here is the guarantee that two workflows
// can never share an id, because an artifact record naming one would then be
// ambiguous about which prompt produced it.
type Registry struct {
	byID map[string]Workflow
}

// NewRegistry builds a registry, refusing an empty set or a duplicate id.
func NewRegistry(workflows ...Workflow) (Registry, error) {
	if len(workflows) == 0 {
		return Registry{}, fmt.Errorf("a workflow registry needs at least one workflow")
	}
	byID := make(map[string]Workflow, len(workflows))
	for _, workflow := range workflows {
		if workflow.ID == "" || workflow.SHA256 == "" {
			return Registry{}, fmt.Errorf("workflow %q was not built by NewWorkflow, so it carries no content hash", workflow.ID)
		}
		if _, clash := byID[workflow.ID]; clash {
			return Registry{}, fmt.Errorf("two workflows share the id %q", workflow.ID)
		}
		byID[workflow.ID] = workflow
	}
	return Registry{byID: byID}, nil
}

// Lookup returns the workflow with this id.
func (r Registry) Lookup(id string) (Workflow, bool) {
	workflow, ok := r.byID[strings.TrimSpace(id)]
	return workflow, ok
}

// IDs lists every workflow id, sorted, so an error message can say what the
// caller could have asked for instead.
func (r Registry) IDs() []string {
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
