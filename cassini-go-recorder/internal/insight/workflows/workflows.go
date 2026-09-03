// Package workflows is the set of prompts this product ships, and the only
// place their bytes live.
//
// # Where the bytes live, and why here (D-718)
//
// Go's embed cannot read outside its own package directory, so "where the
// prompt files sit" is not a filing preference — it decides which package can
// hash them, and therefore which package can prove what a document was made
// with. Three placements were possible and two of them are wrong:
//
//   - Beside the pipeline, in internal/transcribe/templates/, where the summary
//     prompt used to live. That package links the cgo speech recogniser
//     (sherpa-onnx), so a gate over the prompt bytes could only run where a
//     speech toolchain is installed — and the gate has to run in lint.yml,
//     which is the workflow a prompt-only pull request actually triggers
//     (ci.yml's paths-ignore skips '**/*.md'). It would also make the
//     transcription package the owner of prompts the pipeline never runs.
//   - A second copy under internal/insight/. Two copies of a prompt is exactly
//     the drift the content hash exists to catch, installed on purpose.
//   - Here: one copy, in a package that depends on the standard library and the
//     insight seam alone. The pipeline's summary step reads its prompt from
//     this registry rather than embedding its own (internal/transcribe/
//     summary.go), which is what "the summary is an insight like any other"
//     means at the level of bytes — one file, one hash, one home.
//
// So: every prompt this product can send is a file under prompts/, embedded
// here, and referenced by exactly one entry in the table below. There is no
// second copy in the tree, and mirror_test.go fails if one appears and drifts.
//
// # What is not here
//
// Alex's prototype lists five templates. Two of them — "Decisions and open
// questions" and "What changed over time" — have no prompt authored on any
// branch, and a name in this table with no bytes behind it would be a template
// an administrator could select and never run. The panel renders this registry,
// so a workflow whose bytes do not resolve is simply absent. The prototype's
// fifth, "Ask your own question", is not a stored prompt at all: it is the
// caller's own text at run time, which is why insight.QuestionPlaceholder
// exists and why no entry here carries it yet.
//
// A prompt is never edited in place. A change is a new version, a new pair of
// files and a new entry, because two documents claiming the same version and
// disagreeing is worse than either of them. workflows_test.go pins each
// entry's hash so an edit cannot land quietly.
package workflows

import (
	"embed"
	"fmt"
	"path"

	"gocassini/internal/insight"
)

// promptFS holds every prompt file. An FS rather than one string per file so
// the package can enumerate what it embeds and assert that nothing is
// unreferenced: a prompt file nobody registered is a prompt nobody can run,
// and it would still be mirrored, licensed and shipped.
//
//go:embed prompts/*.md
var promptFS embed.FS

// The summarise pair again, by name, for the two accessors the pipeline reads
// at package initialisation (SummarisePromptV0 below). Naming the files in a
// directive rather than resolving them out of the FS by string keeps a rename
// or a deletion a compile error: resolved at run time it would instead be a
// panic during init, which takes down every cassini subcommand — including the
// ones that never summarise anything.
//
//go:embed prompts/summarise.v0.md
var summariseSystemV0 string

//go:embed prompts/summarise-template.v0.md
var summariseTemplateV0 string

// promptDir is where the files sit inside this package.
const promptDir = "prompts"

// OriginBuiltIn is the origin of a workflow compiled into the binary.
//
// It is derived from the resolver rather than written on each entry, because
// the day a second resolver lands — an imported skill bundle, say — the column
// has to be able to say something other than "Built in" without every row
// having been edited to admit it.
const OriginBuiltIn = "Built in"

// The ids a caller names on `cassini insight run --workflow`, and that an
// artifact record carries. They are the prompts' own identities, matching the
// authoring home in skills/, rather than the prototype's UI keys: the id says
// which bytes ran, and the display name is what a person picks by.
const (
	SummariseID      = "summarise"
	SummariseVersion = "v0"
	TodosID          = "todos"
	TodosVersion     = "v0"
)

// spec is one shipped workflow: its identity, the two files its prompt is made
// of, and what the settings panel says about it.
//
// SkillDir names its authoring home under skills/ at the repository root, which
// is what mirror_test.go checks the bytes against. The field is here rather
// than derived from the id because the two vocabularies genuinely differ —
// `todos` is authored in `cassini-meeting-todos` — and a derivation with an
// exception in it is worse than a declaration.
type spec struct {
	ID           string
	Version      string
	SystemFile   string
	TemplateFile string
	SkillDir     string

	// Name is what the panel and the pipeline's step select show.
	Name string
	// Question is what this workflow asks of a set of meetings, in the words a
	// person would use. It is the affordance: the name says nothing about what
	// the model is asked to do.
	Question string
	// Description says what document comes back. It is deliberately about the
	// shape of the output and not a paraphrase of the prompt — a paraphrase
	// drifts from the bytes it summarises, and the panel shows the bytes.
	Description string
}

// shipped is the registry, in the order the panel lists it.
var shipped = []spec{
	{
		ID:           SummariseID,
		Version:      SummariseVersion,
		SystemFile:   "summarise.v0.md",
		TemplateFile: "summarise-template.v0.md",
		SkillDir:     "cassini-meeting-summary",
		Name:         "Meeting summary",
		Question:     "Summarise what happened, what was decided, and what follows.",
		Description:  "One document per meeting in a fixed shape: overview, key points, decisions, action items, open questions and the next step. A section with nothing in it says \"None.\" rather than disappearing. This is the prompt the publish pipeline's summary step runs.",
	},
	{
		ID:           TodosID,
		Version:      TodosVersion,
		SystemFile:   "todos.v0.md",
		TemplateFile: "todos-template.v0.md",
		SkillDir:     "cassini-meeting-todos",
		Name:         "Commitments and owners",
		Question:     "List the commitments and who owns them.",
		Description:  "One section per person who spoke, with what they took on and when they said so, then what was assigned to somebody who never answered, then what nobody claimed. Every participant gets a section even when they took nothing on.",
	},
}

// Entry is one workflow as the settings panel reads it: enough to choose one,
// and enough to know exactly what choosing it sends.
//
// Instruction is the spliced system prompt — the literal bytes that reach the
// model — not a description of it. The prototype showed a plain-language
// paraphrase, which reads well right up to the day it stops matching the
// prompt; nothing can guarantee a paraphrase tracks bytes, so the panel shows
// the bytes and SHA256 says which bytes they were.
type Entry struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Name        string `json:"name"`
	Question    string `json:"question"`
	Description string `json:"description"`
	Origin      string `json:"origin"`
	Instruction string `json:"instruction"`
}

// Registry builds the workflows a run may name.
//
// It fails rather than degrading: a prompt file that will not resolve is a
// workflow that would be offered and could not run, and finding that out at
// the moment somebody asks for a document is finding out too late.
func Registry() (insight.Registry, error) {
	built := make([]insight.Workflow, 0, len(shipped))
	for _, entry := range shipped {
		workflow, err := entry.workflow()
		if err != nil {
			return insight.Registry{}, err
		}
		built = append(built, workflow)
	}
	return insight.NewRegistry(built...)
}

// Catalog is the registry as it is served and displayed, in listing order.
func Catalog() ([]Entry, error) {
	entries := make([]Entry, 0, len(shipped))
	for _, item := range shipped {
		workflow, err := item.workflow()
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{
			ID:          workflow.ID,
			Version:     workflow.Version,
			SHA256:      workflow.SHA256,
			Name:        item.Name,
			Question:    item.Question,
			Description: item.Description,
			Origin:      OriginBuiltIn,
			Instruction: workflow.SystemPrompt(),
		})
	}
	return entries, nil
}

// SummarisePromptV0 and SummariseTemplateV0 hand the pipeline's summary step
// the two halves of its prompt, so that step sends the registry's bytes rather
// than a copy of them (internal/transcribe/summary.go).
//
// They return the raw halves, still carrying {{TEMPLATE}}, because the caller
// splices — the pipeline has always spliced, and reproducing its splice here
// would be a second implementation of the one thing the content hash is taken
// over.
func SummarisePromptV0() string   { return summariseSystemV0 }
func SummariseTemplateV0() string { return summariseTemplateV0 }

// workflow reads this spec's two files and hashes the result.
func (s spec) workflow() (insight.Workflow, error) {
	system, err := readPrompt(s.SystemFile)
	if err != nil {
		return insight.Workflow{}, fmt.Errorf("workflow %q: %w", s.ID, err)
	}
	template, err := readPrompt(s.TemplateFile)
	if err != nil {
		return insight.Workflow{}, fmt.Errorf("workflow %q: %w", s.ID, err)
	}
	return insight.NewWorkflow(insight.WorkflowSpec{
		ID:       s.ID,
		Version:  s.Version,
		System:   system,
		Template: template,
	})
}

func readPrompt(name string) (string, error) {
	raw, err := promptFS.ReadFile(path.Join(promptDir, name))
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", name, err)
	}
	return string(raw), nil
}
