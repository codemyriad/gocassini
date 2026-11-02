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

// summaryWarnings is where a summary template that cannot run says so, for
// the callers outside the pipeline (`meetings summarize`, a speaker edit's
// rewrite). The summary still runs, on the built-in template, and the
// manifest names that one: a meeting without a summary is worse than one in
// the shipped shape, and the manifest records the one that actually ran. The
// pipeline's summary step says it in its own log instead, and records it as
// the step's template_fallback reason.
var summaryWarnings io.Writer = os.Stderr

// ResolveSummaryWorkflow returns the workflow the summary step runs now,
// warning on summaryWarnings when that is the built-in in place of the
// configured template.
func ResolveSummaryWorkflow() insight.Workflow {
	workflow, fallback := resolveSummaryWorkflow()
	if fallback != "" {
		fmt.Fprintf(summaryWarnings, "  warn: %s\n", fallback)
	}
	return workflow
}

// resolveSummaryWorkflow returns the workflow the summary step runs now and,
// when that is the built-in because the configured template cannot run, why
// ("" otherwise).
func resolveSummaryWorkflow() (insight.Workflow, string) {
	id := strings.TrimSpace(os.Getenv(SummaryTemplateEnv))
	if id == "" {
		id = workflows.SummariseID
	}
	var fallback string
	set, err := workflows.Load()
	if err != nil {
		// The templates directory could not be read; the built-ins can.
		fallback = fmt.Sprintf("summary template: %v — using the built-in %s", err, workflows.SummariseID)
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
		fallback = fmt.Sprintf("summary template %q cannot run: %s — using the built-in %s", id, reason, workflows.SummariseID)
	case workflow.TakesQuestion():
		fallback = fmt.Sprintf("summary template %q takes a question, and a summary has nobody to ask — using the built-in %s", id, workflows.SummariseID)
	default:
		return workflow, fallback
	}
	builtIn, _ := set.Registry.Lookup(workflows.SummariseID)
	return builtIn, fallback
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
