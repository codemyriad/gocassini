package transcribe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gocassini/internal/insight"
	"gocassini/internal/insight/workflows"
)

// The summary step's template (D-819).
//
// The publish pipeline's summary is an insight like any other, so it runs a
// template from the same registry `cassini insight run` reads: the built-ins
// plus the custom files in workflows.TemplatesDirEnv. Which one is named by
// SummaryTemplateEnv, which the operator sets from the summary step's
// template setting; unset means the built-in summarise.
//
// The template that wrote a summary is recorded at the top of summary.md as
// front matter, the way an insight document records its workflow, so every
// path that writes a summary — the build, a backfill, a speaker-edit rewrite —
// carries it without each one being told, and every path that packs one reads
// it back (SummaryWorkflowOf) rather than assuming the shipped version.

// SummaryTemplateEnv names the workflow the summary step runs.
const SummaryTemplateEnv = "CASSINI_SUMMARY_TEMPLATE"

// summaryWarnings is where a summary template that cannot run says so. The
// summary still runs, on the built-in template, and its front matter names
// that one: a meeting without a summary is worse than one in the shipped
// shape, and the record stays true either way.
var summaryWarnings io.Writer = os.Stderr

// ResolveSummaryWorkflow returns the workflow the summary step runs now.
func ResolveSummaryWorkflow() insight.Workflow {
	id := strings.TrimSpace(os.Getenv(SummaryTemplateEnv))
	if id == "" {
		id = workflows.SummariseID
	}
	set, err := workflows.Load()
	if err != nil {
		// The templates directory could not be read; the built-ins can.
		fmt.Fprintf(summaryWarnings, "  warn: summary template: %v — using the built-in %s\n", err, workflows.SummariseID)
		set, err = workflows.LoadDir("")
		if err != nil {
			panic(fmt.Sprintf("the built-in workflows do not resolve: %v", err))
		}
	}
	workflow, ok := set.Registry.Lookup(id)
	switch {
	case !ok:
		reason := set.Problem(id)
		if reason == "" {
			reason = "no template has this id"
		}
		fmt.Fprintf(summaryWarnings, "  warn: summary template %q cannot run: %s — using the built-in %s\n", id, reason, workflows.SummariseID)
	case workflow.TakesQuestion():
		fmt.Fprintf(summaryWarnings, "  warn: summary template %q takes a question, and a summary has nobody to ask — using the built-in %s\n", id, workflows.SummariseID)
	default:
		return workflow
	}
	builtIn, _ := set.Registry.Lookup(workflows.SummariseID)
	return builtIn
}

// summaryFrontMatter is the block written above a summary's text.
func summaryFrontMatter(ref insight.WorkflowRef) string {
	return "---\nworkflow:\n" +
		"  id: " + summaryYAMLString(ref.ID) + "\n" +
		"  version: " + summaryYAMLString(ref.Version) + "\n" +
		"  sha256: " + summaryYAMLString(ref.SHA256) + "\n" +
		"---\n\n"
}

func summaryYAMLString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// SummaryWorkflowOf reads the template a summary.md records, and returns the
// summary's text without the block. ok is false for a summary written before
// the block existed, which is returned unchanged.
func SummaryWorkflowOf(markdown string) (ref insight.WorkflowRef, body string, ok bool) {
	text := strings.TrimPrefix(markdown, "\ufeff")
	if !strings.HasPrefix(text, "---\nworkflow:\n") {
		return insight.WorkflowRef{}, markdown, false
	}
	end := strings.Index(text, "\n---\n")
	if end < 0 {
		return insight.WorkflowRef{}, markdown, false
	}
	for _, line := range strings.Split(text[len("---\n"):end], "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		unquoted, err := strconv.Unquote(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		switch key {
		case "id":
			ref.ID = unquoted
		case "version":
			ref.Version = unquoted
		case "sha256":
			ref.SHA256 = unquoted
		}
	}
	if ref.ID == "" {
		return insight.WorkflowRef{}, markdown, false
	}
	return ref, strings.TrimLeft(text[end+len("\n---\n"):], "\n"), true
}
