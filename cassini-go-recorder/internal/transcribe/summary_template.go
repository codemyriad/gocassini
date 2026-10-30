package transcribe

import (
	"fmt"
	"io"
	"os"
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
// The template that wrote a summary is recorded in the manifest, never in
// summary.md, which stays the document and nothing else: BuildMeetingSummary
// returns it, the build writes it into the bundle manifest's "summary" block,
// and pack, `meetings summarize` and backfill carry it into the portable
// file's summary block (templateId, templateVersion, templateSha256).

// SummaryTemplateEnv names the workflow the summary step runs.
const SummaryTemplateEnv = "CASSINI_SUMMARY_TEMPLATE"

// summaryWarnings is where a summary template that cannot run says so. The
// summary still runs, on the built-in template, and the manifest names
// that one: a meeting without a summary is worse than one in the shipped
// shape, and the manifest records the one that actually ran.
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

// SummaryTemplateRecord is the template that wrote a meeting's summary, as a
// manifest records it: the bundle manifest's "summary" block, and the same
// keys in a portable file's summary block (D-819).
type SummaryTemplateRecord struct {
	TemplateID      string `json:"templateId"`
	TemplateVersion string `json:"templateVersion"`
	TemplateSHA256  string `json:"templateSha256"`
}

// SummaryTemplateRecordOf is the record for a workflow ref, or nil for none.
func SummaryTemplateRecordOf(ref insight.WorkflowRef) *SummaryTemplateRecord {
	if ref.ID == "" {
		return nil
	}
	return &SummaryTemplateRecord{TemplateID: ref.ID, TemplateVersion: ref.Version, TemplateSHA256: ref.SHA256}
}
