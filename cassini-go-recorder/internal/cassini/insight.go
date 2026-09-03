package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gocassini/internal/insight"
	"gocassini/internal/insight/workflows"
	"gocassini/internal/meetingcontext"
	"gocassini/internal/transcribe"
)

// `cassini insight run` asks one question of several meetings and keeps the
// answer.
//
// It is the pipeline summary turned inside out. The summary is a step that runs
// unasked, over one meeting, at publish time, and warns and skips when the model
// is unavailable — the right behaviour for a sidecar nobody requested. An
// insight is a document a user asked for by name, so this command does the
// opposite: it runs over as many meetings as it is given, it is addressable
// from outside the pipeline, and it never exits 0 without producing something.
//
// It reads context bundles off disk rather than fetching them, which keeps the
// two credentials apart: `cassini meetings context --json` fetches meetings as
// the user, and this reads what that wrote. That also makes the command usable
// against a bundle a colleague sent you, and testable with no Nextcloud at all.

// Exit codes. The code space is per-command, and these are the outcomes a
// caller has to be able to tell apart without reading the message: whether to
// change a setting, add a credential, or try again.
const (
	// exitInsightNoProvider: no model endpoint is configured.
	exitInsightNoProvider = 3
	// exitInsightRefused: the endpoint answered and refused.
	exitInsightRefused = 4
	// exitInsightModelFailed: the call did not produce a usable answer. The
	// only one of these worth retrying unchanged.
	exitInsightModelFailed = 5
)

// insightProviderFor builds the model provider for a run. Package-level so a
// test can substitute a fake: there is no model endpoint in a test environment,
// and a seam whose failure paths can only be exercised against a live endpoint
// is a seam whose failure paths are never exercised.
var insightProviderFor = func(cfg transcribe.LLMConfig) insight.Provider {
	return llmProvider{cfg: cfg}
}

// llmProvider sends an insight's prompt to the configured OpenAI-compatible
// endpoint.
type llmProvider struct {
	cfg transcribe.LLMConfig
}

func (p llmProvider) Describe() insight.ProviderRef {
	return insight.ProviderRef{
		Kind:    "openai-compatible",
		BaseURL: p.cfg.BaseURL,
		Model:   p.cfg.Model,
	}
}

func (p llmProvider) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := transcribe.ChatCompletion(ctx, p.cfg, system, user)
	if err != nil {
		return "", classifyLLMFailure(err)
	}
	return body, nil
}

// classifyLLMFailure decides which of the two provider failures happened.
//
// A 4xx is the endpoint refusing: an absent or rejected key, a quota, a model
// name it does not serve. Every one of those is fixed by changing something,
// and retrying unchanged will fail identically. Anything else — a 5xx, a
// timeout, an unreachable host — is the call not completing, which retrying is
// a reasonable answer to.
func classifyLLMFailure(err error) error {
	var apiErr *transcribe.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		return insight.Fail(insight.ReasonProviderRefused, err)
	}
	return insight.Fail(insight.ReasonModelFailed, err)
}

// insightWorkflows is the registry of workflows a run may name.
//
// The set and its bytes belong to internal/insight/workflows, which embeds the
// prompt files and hashes them; this command only names one. Every workflow is
// identified by id, version and content hash, because an artifact written
// before those exist can never be told apart from one written by a later edit
// of the same prompt.
func insightWorkflows() (insight.Registry, error) {
	return workflows.Registry()
}

// repeatedPath collects a flag that may be given more than once, keeping the
// caller's order — which is the order the meetings appear in the prompt.
type repeatedPath []string

func (p *repeatedPath) String() string { return strings.Join(*p, ", ") }

func (p *repeatedPath) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("path is empty")
	}
	*p = append(*p, value)
	return nil
}

func runInsight(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printInsightUsage(stdout)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		printInsightUsage(stdout)
		return 0
	case "run":
		return runInsightRun(ctx, args[1:], stdout, stderr)
	case "workflows":
		return runInsightWorkflows(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown insight command %q\n\n", args[0])
		printInsightUsage(stderr)
		return 2
	}
}

func printInsightUsage(w io.Writer) {
	fmt.Fprint(w, `Ask one question of several meetings and keep the answer.

Usage:
  cassini insight run --context ./ctx.json
  cassini insight run --context ./a.json --context ./b.json --out ./Insight.md
  cassini insight run --context ./ctx.json --record ./insight.json
  cassini insight workflows

Commands:
  run        Run one workflow over one or more context bundles
  workflows  List the workflows this build ships, with their versions and hashes
`+"\n")
}

// runInsightWorkflows prints the registry.
//
// It exists so that "which prompts will this deployment actually send?" has an
// answer that does not involve reading the source of the binary you happen to
// have. The operator's GET operator/settings/workflows is this command with
// --json: cassini-operator is a separate Go module and cannot import the
// registry, so the CLI is the only bridge, and one implementation behind both
// is what keeps the panel and the runner from describing different sets.
func runInsightWorkflows(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini insight workflows", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit the registry as JSON, including each workflow's full instruction")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini insight workflows
  cassini insight workflows --json

List the workflows this build ships. Each one is a prompt compiled into this
binary, identified by id, version and the SHA-256 of the exact bytes it sends —
which is what an insight document records, so a document and this listing can be
compared rather than assumed to match.

--json adds each workflow's instruction verbatim: the system prompt with its
template already spliced in, byte for byte as the model receives it.

Naming one on `+"`cassini insight run --workflow <id>`"+` runs it. A workflow
whose bytes do not resolve is not listed and cannot be named, which is the
point: an id you can select is an id that will run.

`+"\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "insight workflows takes no positional arguments, got: %v\n", fs.Args())
		return 2
	}

	entries, err := workflows.Catalog()
	if err != nil {
		fmt.Fprintf(stderr, "insight workflows failed: %v\n", err)
		return 1
	}

	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(entries); err != nil {
			fmt.Fprintf(stderr, "insight workflows failed: write JSON: %v\n", err)
			return 1
		}
		return 0
	}

	// One record per line, as the rest of the CLI prints: a field whose value
	// holds spaces is the last thing on its line, so the output stays readable
	// by eye and by cut -d= -f2-.
	fmt.Fprintf(stdout, "workflows=%d\n", len(entries))
	for _, entry := range entries {
		fmt.Fprintf(stdout, "\nworkflow=%s version=%s sha256=%s origin=%q\n", entry.ID, entry.Version, entry.SHA256, entry.Origin)
		fmt.Fprintf(stdout, "name=%s\n", entry.Name)
		fmt.Fprintf(stdout, "asks=%s\n", entry.Question)
		fmt.Fprintf(stdout, "about=%s\n", entry.Description)
	}
	fmt.Fprintf(stdout, "\nnote=--json adds each workflow's instruction, the exact bytes this build sends to the model\n")
	return 0
}

func runInsightRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var contexts repeatedPath
	fs := flag.NewFlagSet("cassini insight run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&contexts, "context", "path to a cassini.meetings.context.v1 JSON bundle; repeat for several")
	workflowID := fs.String("workflow", workflows.SummariseID, "workflow to run; `cassini insight workflows` lists them")
	model := fs.String("model", "", "override the model the configured endpoint is asked for")
	outPath := fs.String("out", "", "write the insight to this .md file instead of stdout")
	recordPath := fs.String("record", "", "also write the run record to this .json file")
	timestamps := fs.Bool("timestamps", false, "show each passage's start time in the context the model reads, so it can cite where a claim was made")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini insight run --context ./ctx.json
  cassini insight run --context ./a.json --context ./b.json --out ./Insight.md
  cassini insight run --context ./ctx.json --record ./insight.json

Run one workflow over the meetings in one or more context bundles and write the
answer. A bundle is what `+"`cassini meetings context <ids...> --json --out`"+` writes;
each --context adds one, and they reach the model in the order you name them.

The written document carries its own provenance as frontmatter: which meetings
it was produced from, which workflow version and content hash, which endpoint
and model answered, and when. --record writes the same record as JSON beside it,
so a grading harness reads one pair rather than parsing the document.

The endpoint comes from the same environment as `+"`cassini build`"+`, resolved in
layers: LLM_BASE_URL (or OPENROUTER_API_KEY for OpenRouter), then SUMMARY_BASE_URL
/ SUMMARY_MODEL, then INSIGHT_BASE_URL / INSIGHT_MODEL. An insight therefore runs
on the summary endpoint unless it is given one of its own, which is how a
deployment answers a question on a larger model than the one writing every
meeting's summary. CASSINI_SUMMARY_DISABLED does NOT apply: it means "publish
meetings without a summary", not "refuse a document somebody asked for by name".
--model overrides the model for this run alone.

Exit codes say what happened, because a document you asked for and did not get
is not a warning:
  0  the insight was written
  1  the insight was produced but a file could not be written
  2  bad request: the flags, a context file, or the workflow name
  3  no model endpoint is configured
  4  the endpoint refused the request (credential, quota, unknown model)
  5  the model failed or timed out

`+"\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "insight run takes no positional arguments, got: %v\n", fs.Args())
		fmt.Fprintln(stderr, "hint=name each context bundle with its own --context flag")
		return 2
	}
	if len(contexts) == 0 {
		fmt.Fprintln(stderr, "insight run needs at least one --context bundle to read")
		fmt.Fprintln(stderr, "hint=write one with `cassini meetings context <meeting-id>... --json --out ./ctx.json`")
		return 2
	}

	registry, err := insightWorkflows()
	if err != nil {
		fmt.Fprintf(stderr, "insight run failed: build the workflow registry: %v\n", err)
		return 1
	}
	workflow, known := registry.Lookup(*workflowID)
	if !known {
		fmt.Fprintf(stderr, "insight run configuration error: unknown workflow %q, expected one of: %s\n", *workflowID, strings.Join(registry.IDs(), ", "))
		return 2
	}

	bundles, err := readContextBundles(contexts)
	if err != nil {
		fmt.Fprintf(stderr, "insight run configuration error: %v\n", err)
		return 2
	}

	// The same environment `cassini build` reads, resolved the same way, so a
	// host that publishes summaries can run insights without a second thing to
	// configure — and then INSIGHT_* on top, so a deployment that wants a larger
	// model for an ad-hoc question can say so without moving the summary step
	// (D-719). Resolving it here rather than reading SummaryLLM keeps the
	// fallback rule stated in one place, and it is what makes the operator's
	// insight endpoint reach this command at all.
	cfg := transcribe.DefaultInsightLLMConfig()
	if trimmed := strings.TrimSpace(*model); trimmed != "" {
		cfg.Model = trimmed
	}
	if !cfg.IsConfigured() {
		fmt.Fprintln(stderr, "insight run failed: no model endpoint is configured, so there is nothing to ask")
		fmt.Fprintln(stderr, "hint=set LLM_BASE_URL to an OpenAI-compatible endpoint (or OPENROUTER_API_KEY for OpenRouter); SUMMARY_BASE_URL / SUMMARY_MODEL and then INSIGHT_BASE_URL / INSIGHT_MODEL override it, the last giving insights an endpoint of their own")
		return exitInsightNoProvider
	}

	artifact, runErr := insight.Run(ctx, insight.Request{
		Workflow:   workflow,
		Contexts:   bundles,
		RenderOpts: meetingcontext.RenderOpts{Timestamps: *timestamps},
		Provider:   insightProviderFor(cfg),
	})

	// Where the "wrote this file" lines go. Naming a file on stdout is only safe
	// while stdout is not itself the document: with no --out the frontmatter has
	// to start at byte 0 or it is not frontmatter, and a line printed ahead of it
	// silently makes the provenance block inert. `cassini meetings context`
	// guards its own line for the same reason.
	notes := stdout
	if strings.TrimSpace(*outPath) == "" {
		notes = stderr
	}

	// The record is written even for a failed run, and before the exit code is
	// decided: it is the only durable statement of what was attempted, and a
	// harness comparing runs needs the failures as much as the successes.
	recordErr := writeInsightRecord(*recordPath, artifact.Record, notes)

	if runErr != nil {
		fmt.Fprintf(stderr, "insight run failed: %v\n", runErr)
		if recordErr != nil {
			fmt.Fprintf(stderr, "insight run failed: write record: %v\n", recordErr)
		}
		noteStaleInsightDocument(stderr, *outPath)
		return insightExitCode(insight.ReasonOf(runErr))
	}

	document, err := insight.RenderMarkdown(artifact)
	if err != nil {
		fmt.Fprintf(stderr, "insight run failed: %v\n", err)
		return 1
	}
	out, closeOut, err := openMeetingsOutput(*outPath, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "insight run failed: %v\n", err)
		return 1
	}
	_, writeErr := io.WriteString(out, document)
	if closeErr := closeOut(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		fmt.Fprintf(stderr, "insight run failed: write output: %v\n", writeErr)
		return 1
	}
	if strings.TrimSpace(*outPath) != "" {
		fmt.Fprintf(stdout, "insight -> %s\n", *outPath)
	}
	// Reported only now, because an answer the model already produced is not
	// thrown away over a side file: the document is written first and exit 1
	// still means "produced but could not be written" — of the record, this time.
	if recordErr != nil {
		fmt.Fprintf(stderr, "insight run failed: write record: %v\n", recordErr)
		return 1
	}
	return 0
}

// noteStaleInsightDocument says so when a failed run leaves an earlier run's
// document sitting at --out.
//
// The record beside it has just been overwritten with this failure, so the pair
// describes two different runs and nothing in either file says which. The file
// is not removed: it is the caller's, this run did not write it, and it may be
// the only copy of an answer that cost a model call.
func noteStaleInsightDocument(stderr io.Writer, outPath string) {
	outPath = strings.TrimSpace(outPath)
	if outPath == "" {
		return
	}
	if _, err := os.Stat(outPath); err != nil {
		return
	}
	fmt.Fprintf(stderr, "warning=%s is an earlier run's document and was left as it is; it does not describe this run\n", outPath)
}

// insightExitCode maps a failure reason to the code a caller reads.
func insightExitCode(reason insight.Reason) int {
	switch reason {
	case insight.ReasonBadRequest:
		return 2
	case insight.ReasonNoProvider:
		return exitInsightNoProvider
	case insight.ReasonProviderRefused:
		return exitInsightRefused
	default:
		return exitInsightModelFailed
	}
}

// readContextBundles decodes every named bundle, in the order given.
//
// One unreadable bundle fails the whole run, matching `cassini meetings
// context`: an answer assembled from the bundles that happened to load is an
// answer to a different question than the one that was asked, and it looks
// right.
func readContextBundles(paths []string) ([]meetingcontext.Bundle, error) {
	bundles := make([]meetingcontext.Bundle, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read context bundle %s: %w", path, err)
		}
		bundle, err := meetingcontext.DecodeJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("read context bundle %s: %w", path, err)
		}
		bundles = append(bundles, bundle)
	}
	return bundles, nil
}

func writeInsightRecord(path string, record insight.Record, notes io.Writer) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	encodeErr := insight.EncodeRecordJSON(file, record)
	if closeErr := file.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if encodeErr != nil {
		return encodeErr
	}
	fmt.Fprintf(notes, "insight_record -> %s\n", path)
	return nil
}
