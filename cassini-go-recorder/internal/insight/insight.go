// Package insight runs one workflow over one or more meetings and returns the
// document it produced, together with a record of what produced it.
//
// # What this package is not allowed to do
//
// Run splices a prompt, joins rendered context bundles, makes one completion
// call through an injected provider, and returns bytes. It does not read the
// filesystem, shell out to ffprobe, talk to Nextcloud, know the operator
// exists, or store anything. That is the requirement, not a style preference:
// the operator is a separate Go module that cannot import this one, and the
// only bridge across that boundary is the `cassini` binary. A seam that
// reached for the pipeline would drag the pipeline through the bridge with it,
// and there is no bridge wide enough. It is also what makes an insight run
// testable with no model endpoint at all — a fake provider exercises every
// path, which matters because the environment this is developed in has none.
//
// # Decisions fixed here, because later work builds on them
//
// Status vocabulary: queued, running, succeeded, failed. Run itself is
// synchronous and can only ever return succeeded or failed; queued and running
// exist because a stored run record is created before its content is (D-720),
// and a card that appears a minute after the button was pressed is a worse
// answer than one that says it is running. This supersedes the earlier
// ok/skipped/failed wording, which was a pipeline-sidecar vocabulary: "skipped"
// is a reasonable thing for a summary nobody asked for, and never a reasonable
// thing for a document a user asked for by name.
//
// Artifact ids: "ins_" followed by sixteen lowercase hex characters, matching
// the prefix-plus-hex scheme the product already uses for rooms (rm_) and
// meetings (mtg_). Deliberately random rather than derived from the inputs: two
// deliberate runs of the same workflow over the same meetings are two
// artifacts, never one, because published documents are append-only and a
// content-derived id would make the second silently the first. A retry is the
// other case — same id, another attempt — which is why the id has to be stable
// across attempts and therefore cannot encode the attempt.
//
// Failure is classified rather than flattened, because the answers differ:
// nothing configured, the provider refused, the model failed, the request was
// bad. `cassini insight run` turns each into its own exit code.
package insight

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gocassini/internal/meetingcontext"
)

// RecordVersion identifies the artifact record's shape. It is versioned from
// the start for the same reason the context bundle is: it is written into files
// that outlive the code that wrote them.
const RecordVersion = "cassini.insight.record.v1"

// Status is the state of an insight run.
//
// StatusQueued and StatusRunning are never returned by Run — it is synchronous
// — and are defined here so that the store which does use them (D-720) and the
// document which records the outcome speak one vocabulary rather than two.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Reason says why a run failed, in the four kinds that need different answers
// from whoever reads it.
type Reason string

const (
	// ReasonBadRequest: the request could not be run as asked — no context, an
	// unknown workflow, a question a workflow has no slot for. Nothing was sent
	// anywhere.
	ReasonBadRequest Reason = "bad-request"
	// ReasonNoProvider: no model endpoint is configured. The fix is a setting,
	// not a retry.
	ReasonNoProvider Reason = "no-provider"
	// ReasonProviderRefused: the endpoint answered and said no — a missing or
	// rejected key, a quota, a policy. The fix is a credential or a different
	// endpoint.
	ReasonProviderRefused Reason = "provider-refused"
	// ReasonModelFailed: the call did not produce a usable answer — a timeout,
	// an unreachable host, a server error, an empty reply. A retry is a
	// sensible response to this one and to no other.
	ReasonModelFailed Reason = "model-failed"
)

// Failure is a run failure with its reason attached.
//
// Callers classify with ReasonOf rather than by matching error text, which is
// the whole point: `cassini insight run` maps the reason to an exit code, and
// an exit code derived from a message would change whenever a message did.
type Failure struct {
	Reason Reason
	Err    error
}

func (f *Failure) Error() string {
	if f.Err == nil {
		return string(f.Reason)
	}
	return f.Err.Error()
}

func (f *Failure) Unwrap() error { return f.Err }

// Fail wraps err with a reason.
func Fail(reason Reason, err error) error {
	if err == nil {
		return &Failure{Reason: reason, Err: errors.New(string(reason))}
	}
	return &Failure{Reason: reason, Err: err}
}

// Failf wraps a formatted message with a reason.
func Failf(reason Reason, format string, args ...any) error {
	return &Failure{Reason: reason, Err: fmt.Errorf(format, args...)}
}

// ReasonOf reports the reason carried by err.
//
// An error with no reason on it is ReasonModelFailed: an unclassified failure
// came from the provider, which is the only part of a run that can fail in ways
// this package did not enumerate.
func ReasonOf(err error) Reason {
	if err == nil {
		return ""
	}
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Reason
	}
	return ReasonModelFailed
}

// ProviderRef names the endpoint and model a run actually resolved to, as
// opposed to the one it asked for. A retry re-resolves both from current
// settings — that is what makes "add a key" a fix rather than a suggestion —
// so the record has to describe the attempt that produced the bytes.
type ProviderRef struct {
	Kind    string `json:"kind"`
	BaseURL string `json:"baseUrl,omitempty"`
	Model   string `json:"model,omitempty"`
}

// Provider is the one thing a run cannot do by itself: send a prompt to a model
// and get bytes back.
//
// It is an interface rather than a bare function because the record has to say
// what produced the bytes, and a function cannot describe itself. Everything
// HTTP-shaped lives behind it, which is what lets every path in this package be
// exercised by a fake with no network and no endpoint.
type Provider interface {
	// Describe says which endpoint and model this provider resolved to.
	Describe() ProviderRef
	// Complete sends one system and user message and returns the reply. An
	// error should carry a Reason (see Fail) when the provider knows one;
	// anything else is read as ReasonModelFailed.
	Complete(ctx context.Context, system, user string) (string, error)
}

// Request is one insight run.
type Request struct {
	// Workflow is the prompt to run. Build it with NewWorkflow so it carries a
	// content hash.
	Workflow Workflow
	// Contexts are the meetings to run it over, in the order they should appear
	// in the prompt. Several bundles, each of which may itself hold several
	// meetings: `cassini meetings context A B C --json` writes one file, and N
	// of those files are N bundles.
	Contexts []meetingcontext.Bundle
	// Question is the freeform question, for a workflow that takes one. Empty
	// for a workflow that does not, and the mismatch is refused both ways.
	Question string
	// RenderOpts controls how the bundles are rendered into the prompt. It is
	// recorded, because whether the model could see timestamps decides whether
	// a citation in its answer means anything.
	RenderOpts meetingcontext.RenderOpts
	// Provider is the model endpoint. Required.
	Provider Provider

	// Now and NewID exist so a test can pin the two values that would otherwise
	// make every record different. Both default to the real thing.
	Now   func() time.Time
	NewID func() (string, error)
}

// MeetingRef is one meeting an artifact was produced from.
//
// The room travels with the id because an insight over several meetings is
// routinely an insight across several rooms, and an id alone cannot be read by
// the person who asked for it.
type MeetingRef struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	RoomID   string `json:"roomId,omitempty"`
	RoomName string `json:"roomName,omitempty"`
}

// WorkflowRef is the prompt's identity as recorded in an artifact.
type WorkflowRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// ContextRef describes what the model was actually shown.
//
// SHA256 is taken over the assembled prompt input, not over the source files:
// it is the thing that changes when a bundle is re-exported with different
// options or a different renderer, which is exactly the drift a record exists
// to make visible.
type ContextRef struct {
	Version    string       `json:"version"`
	SHA256     string       `json:"sha256"`
	Bundles    int          `json:"bundles"`
	Timestamps bool         `json:"timestamps"`
	Meetings   []MeetingRef `json:"meetings"`
}

// Record is the provenance an artifact carries: which meetings, which prompt,
// which model, when, and how it ended.
//
// It is written twice — as frontmatter in the document, and as JSON under
// --record — from this one struct, so a reader and an eval harness can never be
// told two different stories about the same run.
type Record struct {
	Version    string `json:"version"`
	ArtifactID string `json:"artifactId"`
	Status     Status `json:"status"`
	// Reason and Error are set only on a failed run.
	Reason Reason `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`

	Workflow WorkflowRef `json:"workflow"`
	Provider ProviderRef `json:"provider"`
	Context  ContextRef  `json:"context"`
	Question string      `json:"question,omitempty"`

	StartedAtUTC  string `json:"startedAtUtc"`
	FinishedAtUTC string `json:"finishedAtUtc"`
}

// Artifact is what a run produced: the document, and the record of what
// produced it.
type Artifact struct {
	Record Record
	// Body is the model's answer, with no frontmatter. Empty unless the record
	// says succeeded.
	Body string
}

// bundleSeparator divides consecutive context bundles in the assembled prompt.
// The same thematic break meetingcontext puts between meetings, for the same
// reason: the model is reading one document, and a bundle boundary is not a
// different kind of boundary from a meeting boundary.
const bundleSeparator = "\n\n---\n\n"

// Run executes one insight.
//
// It always returns a Record — a failed run's record says why it failed, which
// is what a caller persists and a user reads — but a caller must decide on the
// error, never on the record: the Body is empty on failure and RenderMarkdown
// refuses to write a document for a record that is not succeeded.
func Run(ctx context.Context, req Request) (Artifact, error) {
	now := req.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := req.NewID
	if newID == nil {
		newID = NewArtifactID
	}

	startedAt := now().UTC()
	record := Record{
		Version:      RecordVersion,
		Status:       StatusFailed,
		Workflow:     req.Workflow.Ref(),
		Question:     strings.TrimSpace(req.Question),
		StartedAtUTC: formatTime(startedAt),
	}
	fail := func(err error) (Artifact, error) {
		record.Reason = ReasonOf(err)
		record.Error = err.Error()
		record.FinishedAtUTC = formatTime(now().UTC())
		return Artifact{Record: record}, err
	}

	id, err := newID()
	if err != nil {
		return fail(Failf(ReasonBadRequest, "%v", err))
	}
	record.ArtifactID = id

	if req.Provider == nil {
		return fail(Failf(ReasonNoProvider, "this run has no model provider, so there is nothing to ask"))
	}
	record.Provider = req.Provider.Describe()

	if req.Workflow.SHA256 == "" {
		return fail(Failf(ReasonBadRequest, "workflow %q carries no content hash, so it was not built by NewWorkflow", req.Workflow.ID))
	}

	system, err := assemblePrompt(req.Workflow, record.Question)
	if err != nil {
		return fail(err)
	}

	user, meetings, err := assembleContext(req.Contexts, req.RenderOpts)
	if err != nil {
		return fail(err)
	}
	digest := sha256.Sum256([]byte(user))
	record.Context = ContextRef{
		Version:    meetingcontext.Version,
		SHA256:     hex.EncodeToString(digest[:]),
		Bundles:    len(req.Contexts),
		Timestamps: req.RenderOpts.Timestamps,
		Meetings:   meetings,
	}

	body, err := req.Provider.Complete(ctx, system, user)
	if err != nil {
		return fail(err)
	}
	body = trimFence(strings.TrimSpace(body))
	if body == "" {
		return fail(Failf(ReasonModelFailed, "the model returned an empty document"))
	}

	record.Status = StatusSucceeded
	record.FinishedAtUTC = formatTime(now().UTC())
	return Artifact{Record: record, Body: body + "\n"}, nil
}

// assemblePrompt resolves the template and the question into the system prompt.
func assemblePrompt(workflow Workflow, question string) (string, error) {
	system := workflow.systemPrompt()
	switch {
	case workflow.TakesQuestion() && question == "":
		return "", Failf(ReasonBadRequest, "workflow %q asks a question of the meetings and none was given", workflow.ID)
	case !workflow.TakesQuestion() && question != "":
		return "", Failf(ReasonBadRequest, "workflow %q takes no question, so the question given would be dropped without being asked", workflow.ID)
	case workflow.TakesQuestion():
		system = strings.ReplaceAll(system, QuestionPlaceholder, question)
	}
	return system, nil
}

// assembleContext renders the bundles into the user message and collects the
// meetings they name.
//
// A bundle with no meetings is refused rather than skipped. The whole reason
// this seam takes bundles instead of pipeline structs is that a question asked
// of several meetings must be asked of all of them, and a run that quietly
// dropped one would answer from a subset and look right doing it.
func assembleContext(bundles []meetingcontext.Bundle, opts meetingcontext.RenderOpts) (string, []MeetingRef, error) {
	if len(bundles) == 0 {
		return "", nil, Failf(ReasonBadRequest, "this run has no meetings to read")
	}
	parts := make([]string, 0, len(bundles))
	meetings := make([]MeetingRef, 0, len(bundles))
	for i, bundle := range bundles {
		rendered, err := meetingcontext.RenderMarkdown(bundle, opts)
		if err != nil {
			return "", nil, Failf(ReasonBadRequest, "context %d of %d: %v", i+1, len(bundles), err)
		}
		parts = append(parts, strings.TrimRight(rendered, "\n"))
		for _, meeting := range bundle.Meetings {
			meetings = append(meetings, MeetingRef{
				ID:       meeting.Meeting.ID,
				Title:    meeting.Meeting.Title,
				RoomID:   meeting.Meeting.RoomID,
				RoomName: meeting.Meeting.RoomName,
			})
		}
	}
	return strings.Join(parts, bundleSeparator) + "\n", meetings, nil
}

// NewArtifactID returns a fresh artifact id. See the package comment for why it
// is random rather than derived from the run's inputs.
func NewArtifactID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate an insight id: %w", err)
	}
	return "ins_" + hex.EncodeToString(buf[:]), nil
}

// formatTime writes a UTC timestamp the way every other Cassini record does.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// trimFence removes a single surrounding ```…``` fence.
//
// Deliberately a second implementation of the pipeline's stripMarkdownFences
// rather than a shared one: sharing it would mean importing internal/transcribe
// here, and that package links the cgo speech recogniser — a dependency this
// package exists to be free of, and one that would make these tests require a
// speech toolchain to run. Inner fences are left alone; a fenced answer is a
// model ignoring an instruction, not a document with code in it.
func trimFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	rest = strings.TrimRight(rest, " \n\t")
	rest = strings.TrimSuffix(rest, "```")
	return strings.TrimSpace(rest)
}
