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
// so a workflow whose bytes do not resolve is simply absent.
//
// The prototype's fifth, "Ask your own question", IS here (AskID). It is still
// a stored prompt like any other — the thing that makes it freeform is that its
// system prompt carries insight.QuestionPlaceholder, so Run splices the
// caller's own text into it and Workflow.TakesQuestion reports true. That is
// the whole of the mechanism: a workflow "takes a question" because its bytes
// have somewhere to put one, not because a flag on this table says so. It is
// what lets the app offer a question box for exactly the templates that can use
// one, and refuse a question given to one that cannot.
//
// A prompt is never edited in place. A change is a new version, a new pair of
// files and a new entry, because two documents claiming the same version and
// disagreeing is worse than either of them. workflows_test.go pins each
// entry's hash so an edit cannot land quietly.
package workflows

import (
	"embed"
	"fmt"
	"os"
	"path"
	"strings"

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
	AskID            = "ask"
	AskVersion       = "v0"
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
	// Description is one sentence, shown to the person choosing the template,
	// saying what it does and what comes back. Built-ins and custom templates
	// have the same three visible parts: name, description, prompt. It stays
	// about the job and the shape of the output rather than paraphrasing the
	// prompt, which drifts from the bytes; the panel shows the bytes.
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
		Description:  "Summarises what happened, what was decided and what follows: overview, key points, decisions, actions, open questions and next steps.",
	},
	{
		ID:           TodosID,
		Version:      TodosVersion,
		SystemFile:   "todos.v0.md",
		TemplateFile: "todos-template.v0.md",
		SkillDir:     "cassini-meeting-todos",
		Name:         "Commitments and owners",
		Description:  "Lists what each person committed to and by when, plus anything unassigned.",
	},
	{
		ID:           AskID,
		Version:      AskVersion,
		SystemFile:   "ask.v0.md",
		TemplateFile: "ask-template.v0.md",
		SkillDir:     "cassini-meeting-ask",
		Name:         "Ask your own question",
		// Whether the app shows a question box comes from the {{QUESTION}} slot
		// in this entry's bytes (Workflow.TakesQuestion), not from this table.
		Description: "Answers a question of your own about the meetings you picked, with who said what and what they leave unanswered.",
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
	Description string `json:"description"`
	Origin      string `json:"origin"`
	Instruction string `json:"instruction"`
	// Error is set on a custom template that cannot run, and says why. Such an
	// entry carries no hash and is not in the registry; it is listed so the
	// panel can show the administrator what is wrong with the file.
	Error string `json:"error,omitempty"`
}

// Set is everything a deployment can run, and everything it was asked to run
// and cannot: the registry, the catalogue that lists it, and the reason each
// broken custom template is broken.
type Set struct {
	Registry insight.Registry
	Entries  []Entry
	// DirError says why the templates directory could not be read, or "" when
	// it was read or none is configured. The built-ins are still in the set:
	// custom templates are then unknown, not every template.
	DirError string
}

// Problem says why the custom template id cannot run, or "" when nothing is
// known to be wrong with it: the reason a listed broken file gives, or, for
// an id the set does not hold, that the templates directory could not be read.
func (s Set) Problem(id string) string {
	id = strings.TrimSpace(id)
	for _, entry := range s.Entries {
		if entry.ID == id && entry.Error != "" {
			return entry.Error
		}
	}
	if _, ok := s.Registry.Lookup(id); !ok && s.DirError != "" {
		return "the custom templates could not be read: " + s.DirError
	}
	return ""
}

// Load builds the set from the shipped table and the directory TemplatesDirEnv
// names.
func Load() (Set, error) {
	return LoadDir(os.Getenv(TemplatesDirEnv))
}

// LoadDir builds the set from the shipped table and the custom templates in
// dir ("" for built-ins only).
//
// A built-in that will not resolve fails the whole set, as it always has: that
// is a broken build. A custom file that will not resolve is listed with its
// error and left out of the registry, because one bad file an administrator
// dropped in must not stop every other template, the summary step included,
// from running. A templates directory that cannot be read is the same rule
// one level up: the built-ins load, and Set.DirError says why no custom
// template is there.
func LoadDir(dir string) (Set, error) {
	built := make([]insight.Workflow, 0, len(shipped))
	entries := make([]Entry, 0, len(shipped))
	for _, item := range shipped {
		workflow, err := item.workflow()
		if err != nil {
			return Set{}, err
		}
		built = append(built, workflow)
		entries = append(entries, Entry{
			ID:          workflow.ID,
			Version:     workflow.Version,
			SHA256:      workflow.SHA256,
			Name:        item.Name,
			Description: item.Description,
			Origin:      OriginBuiltIn,
			Instruction: workflow.SystemPrompt(),
		})
	}

	custom, dirErr := readCustomDir(dir)
	var dirError string
	if dirErr != nil {
		dirError = dirErr.Error()
	}
	for _, file := range custom {
		entry := Entry{
			ID:          file.template.ID,
			Name:        file.template.Name,
			Description: file.template.Description,
			Origin:      OriginCustom,
			Instruction: file.template.Instruction,
		}
		if file.err != nil {
			if strings.TrimSpace(entry.Name) == "" {
				entry.Name = entry.ID
			}
			if file.template.Version > 0 {
				entry.Version = file.template.VersionLabel()
			}
			entry.Error = file.err.Error()
			entries = append(entries, entry)
			continue
		}
		entry.Version = file.workflow.Version
		entry.SHA256 = file.workflow.SHA256
		entry.Instruction = file.workflow.SystemPrompt()
		built = append(built, file.workflow)
		entries = append(entries, entry)
	}

	registry, err := insight.NewRegistry(built...)
	if err != nil {
		return Set{}, err
	}
	return Set{Registry: registry, Entries: entries, DirError: dirError}, nil
}

// Registry builds the workflows a run may name: the shipped ones, and the
// custom templates in TemplatesDirEnv that resolve.
//
// It fails rather than degrading on a built-in: a prompt file that will not
// resolve is a workflow that would be offered and could not run, and finding
// that out at the moment somebody asks for a document is finding out too late.
func Registry() (insight.Registry, error) {
	set, err := Load()
	if err != nil {
		return insight.Registry{}, err
	}
	return set.Registry, nil
}

// Catalog is the registry as it is served and displayed, in listing order:
// built-ins first, then custom templates by id, broken ones included with
// their Error.
func Catalog() ([]Entry, error) {
	set, err := Load()
	if err != nil {
		return nil, err
	}
	return set.Entries, nil
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
