package insight

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gocassini/internal/meetingcontext"
)

// fakeProvider stands in for a model endpoint.
//
// Every test in this package runs through it: there is no LLM endpoint in a
// test environment, and a seam whose failure paths need a live model to reach
// is a seam whose failure paths are never checked.
type fakeProvider struct {
	ref    ProviderRef
	reply  string
	err    error
	system string
	user   string
	calls  int
}

func (p *fakeProvider) Describe() ProviderRef { return p.ref }

func (p *fakeProvider) Complete(_ context.Context, system, user string) (string, error) {
	p.calls++
	p.system = system
	p.user = user
	if p.err != nil {
		return "", p.err
	}
	return p.reply, nil
}

func testProvider(reply string) *fakeProvider {
	return &fakeProvider{
		ref:   ProviderRef{Kind: "fake", BaseURL: "http://model.invalid/v1", Model: "test-model"},
		reply: reply,
	}
}

func testWorkflow(t *testing.T, system, template string) Workflow {
	t.Helper()
	workflow, err := NewWorkflow(WorkflowSpec{ID: "summarise", Version: "v0", System: system, Template: template})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}
	return workflow
}

func testBundle(meetings ...meetingcontext.BuildInput) meetingcontext.Bundle {
	bundle := meetingcontext.Bundle{}
	for _, in := range meetings {
		bundle.Meetings = append(bundle.Meetings, meetingcontext.Build(in))
	}
	return bundle
}

func meeting(id, title, roomID, roomName string) meetingcontext.BuildInput {
	return meetingcontext.BuildInput{
		ID:        id,
		Title:     title,
		RoomID:    roomID,
		RoomName:  roomName,
		WordCount: 4,
		Segments: []meetingcontext.Segment{
			{SpeakerLabel: "Ada", StartMS: 61000, EndMS: 64000, Text: "We shipped it on " + title + "."},
		},
	}
}

// fixedRun pins the two values that would otherwise differ on every run, so a
// record can be asserted field by field.
func fixedRun(req Request) Request {
	req.Now = func() time.Time { return time.Date(2026, 9, 3, 9, 30, 0, 0, time.UTC) }
	req.NewID = func() (string, error) { return "ins_0123456789abcdef", nil }
	return req
}

// The shape D-656 exists to produce: several meetings from several rooms, one
// question, one document that can say where its answer came from.
func TestRunOverSeveralMeetingsRecordsEveryOne(t *testing.T) {
	provider := testProvider("# Answer\n\nThe three meetings agreed.\n")
	workflow := testWorkflow(t, "Follow the template.\n\n{{TEMPLATE}}\n", "# Answer\n")

	artifact, err := Run(context.Background(), fixedRun(Request{
		Workflow: workflow,
		Contexts: []meetingcontext.Bundle{
			testBundle(meeting("mtg_a", "Monday", "rm_one", "Planning")),
			testBundle(
				meeting("mtg_b", "Tuesday", "rm_two", "Delivery"),
				meeting("mtg_c", "Wednesday", "rm_two", "Delivery"),
			),
		},
		Provider: provider,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if artifact.Record.Status != StatusSucceeded {
		t.Fatalf("status = %q, want %q", artifact.Record.Status, StatusSucceeded)
	}
	if artifact.Record.ArtifactID != "ins_0123456789abcdef" {
		t.Errorf("artifact id = %q", artifact.Record.ArtifactID)
	}
	if artifact.Body != "# Answer\n\nThe three meetings agreed.\n" {
		t.Errorf("body = %q", artifact.Body)
	}
	if artifact.Record.Version != RecordVersion {
		t.Errorf("record version = %q, want %q", artifact.Record.Version, RecordVersion)
	}
	if artifact.Record.StartedAtUTC != "2026-09-03T09:30:00Z" || artifact.Record.FinishedAtUTC != "2026-09-03T09:30:00Z" {
		t.Errorf("timestamps = %q .. %q", artifact.Record.StartedAtUTC, artifact.Record.FinishedAtUTC)
	}

	want := []MeetingRef{
		{ID: "mtg_a", Title: "Monday", RoomID: "rm_one", RoomName: "Planning"},
		{ID: "mtg_b", Title: "Tuesday", RoomID: "rm_two", RoomName: "Delivery"},
		{ID: "mtg_c", Title: "Wednesday", RoomID: "rm_two", RoomName: "Delivery"},
	}
	if len(artifact.Record.Context.Meetings) != len(want) {
		t.Fatalf("meetings = %+v, want %d", artifact.Record.Context.Meetings, len(want))
	}
	for i, got := range artifact.Record.Context.Meetings {
		if got != want[i] {
			t.Errorf("meeting %d = %+v, want %+v", i, got, want[i])
		}
	}
	if artifact.Record.Context.Bundles != 2 {
		t.Errorf("bundles = %d, want 2", artifact.Record.Context.Bundles)
	}
	if artifact.Record.Context.Version != meetingcontext.Version {
		t.Errorf("context version = %q", artifact.Record.Context.Version)
	}
	if len(artifact.Record.Context.SHA256) != 64 {
		t.Errorf("context sha256 = %q, want a hex digest", artifact.Record.Context.SHA256)
	}

	if artifact.Record.Workflow != workflow.Ref() {
		t.Errorf("workflow ref = %+v, want %+v", artifact.Record.Workflow, workflow.Ref())
	}
	if artifact.Record.Provider != provider.ref {
		t.Errorf("provider ref = %+v, want %+v", artifact.Record.Provider, provider.ref)
	}

	// Every meeting reaches the prompt, in the order the caller asked for, and
	// the bundles are joined the way meetingcontext joins meetings.
	for _, id := range []string{"mtg_a", "mtg_b", "mtg_c"} {
		if !strings.Contains(provider.user, id) {
			t.Errorf("prompt is missing %s:\n%s", id, provider.user)
		}
	}
	if strings.Index(provider.user, "mtg_a") > strings.Index(provider.user, "mtg_b") {
		t.Error("the meetings reached the prompt out of the caller's order")
	}
	if strings.Count(provider.user, "\n\n---\n\n") != 2 {
		t.Errorf("expected two separators between three meetings:\n%s", provider.user)
	}
	if provider.system != "Follow the template.\n\n# Answer\n\n" {
		t.Errorf("system prompt = %q", provider.system)
	}
}

// The hash is over what the model was shown, so a re-export that renders
// differently is visible even when the meetings are identical.
func TestContextHashCoversWhatTheModelWasShown(t *testing.T) {
	workflow := testWorkflow(t, "Answer.\n\n{{TEMPLATE}}", "# Answer")
	bundles := []meetingcontext.Bundle{testBundle(meeting("mtg_a", "Monday", "rm_one", "Planning"))}

	run := func(opts meetingcontext.RenderOpts) Record {
		t.Helper()
		artifact, err := Run(context.Background(), fixedRun(Request{
			Workflow:   workflow,
			Contexts:   bundles,
			RenderOpts: opts,
			Provider:   testProvider("ok"),
		}))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return artifact.Record
	}

	plain := run(meetingcontext.RenderOpts{})
	again := run(meetingcontext.RenderOpts{})
	cited := run(meetingcontext.RenderOpts{Timestamps: true})

	if plain.Context.SHA256 != again.Context.SHA256 {
		t.Error("the same context hashed differently twice")
	}
	if plain.Context.SHA256 == cited.Context.SHA256 {
		t.Error("timestamps changed what the model was shown but not the hash")
	}
	if plain.Context.Timestamps || !cited.Context.Timestamps {
		t.Errorf("the record does not say whether the model could see timestamps: %v / %v", plain.Context.Timestamps, cited.Context.Timestamps)
	}
}

func TestRunClassifiesFailures(t *testing.T) {
	workflow := testWorkflow(t, "Answer.\n\n{{TEMPLATE}}", "# Answer")
	bundles := []meetingcontext.Bundle{testBundle(meeting("mtg_a", "Monday", "", ""))}

	refused := testProvider("")
	refused.err = Fail(ReasonProviderRefused, errors.New("API returned 401: no key"))
	broken := testProvider("")
	broken.err = errors.New("dial tcp: connection refused")

	cases := []struct {
		name    string
		request Request
		want    Reason
	}{
		{"no provider at all", Request{Workflow: workflow, Contexts: bundles}, ReasonNoProvider},
		{"no meetings", Request{Workflow: workflow, Provider: testProvider("ok")}, ReasonBadRequest},
		{"a bundle with nothing in it", Request{Workflow: workflow, Contexts: []meetingcontext.Bundle{{}}, Provider: testProvider("ok")}, ReasonBadRequest},
		{"an unhashed workflow", Request{Workflow: Workflow{ID: "x", Version: "v0", System: "s"}, Contexts: bundles, Provider: testProvider("ok")}, ReasonBadRequest},
		{"the endpoint refused", Request{Workflow: workflow, Contexts: bundles, Provider: refused}, ReasonProviderRefused},
		{"the call did not complete", Request{Workflow: workflow, Contexts: bundles, Provider: broken}, ReasonModelFailed},
		{"an empty answer", Request{Workflow: workflow, Contexts: bundles, Provider: testProvider("   \n")}, ReasonModelFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := Run(context.Background(), fixedRun(tc.request))
			if err == nil {
				t.Fatalf("Run succeeded, want %s", tc.want)
			}
			if got := ReasonOf(err); got != tc.want {
				t.Fatalf("reason = %q, want %q (%v)", got, tc.want, err)
			}
			if artifact.Body != "" {
				t.Errorf("a failed run produced a body: %q", artifact.Body)
			}
			// A failed run still describes itself: that record is what a store
			// keeps and what a user is shown instead of a spinner that stops.
			if artifact.Record.Status != StatusFailed {
				t.Errorf("status = %q, want %q", artifact.Record.Status, StatusFailed)
			}
			if artifact.Record.Reason != tc.want {
				t.Errorf("record reason = %q, want %q", artifact.Record.Reason, tc.want)
			}
			if artifact.Record.Error == "" {
				t.Error("the record does not say what went wrong")
			}
			if artifact.Record.FinishedAtUTC == "" {
				t.Error("the record does not say when it ended")
			}
		})
	}
}

// A question and a workflow that takes one have to match, in both directions:
// the silent versions are a question that is never asked and a prompt asking
// about nothing.
func TestRunRefusesAQuestionMismatch(t *testing.T) {
	bundles := []meetingcontext.Bundle{testBundle(meeting("mtg_a", "Monday", "", ""))}
	noQuestion := testWorkflow(t, "Summarise.\n\n{{TEMPLATE}}", "# Answer")
	asksOne, err := NewWorkflow(WorkflowSpec{ID: "ask", Version: "v0", System: "Answer this: {{QUESTION}}"})
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}

	if _, err := Run(context.Background(), fixedRun(Request{Workflow: noQuestion, Contexts: bundles, Question: "what changed?", Provider: testProvider("ok")})); ReasonOf(err) != ReasonBadRequest {
		t.Errorf("a question given to a workflow that takes none: %v", err)
	}
	if _, err := Run(context.Background(), fixedRun(Request{Workflow: asksOne, Contexts: bundles, Provider: testProvider("ok")})); ReasonOf(err) != ReasonBadRequest {
		t.Errorf("a workflow that takes a question, run without one: %v", err)
	}

	provider := testProvider("answered")
	artifact, err := Run(context.Background(), fixedRun(Request{Workflow: asksOne, Contexts: bundles, Question: "what changed?", Provider: provider}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.system != "Answer this: what changed?" {
		t.Errorf("system prompt = %q", provider.system)
	}
	if artifact.Record.Question != "what changed?" {
		t.Errorf("the record does not carry the question: %q", artifact.Record.Question)
	}
}

// Models return fenced markdown despite being told not to, and a document that
// opens with ``` is not the document that was asked for.
func TestRunTrimsASurroundingFence(t *testing.T) {
	workflow := testWorkflow(t, "Answer.\n\n{{TEMPLATE}}", "# Answer")
	bundles := []meetingcontext.Bundle{testBundle(meeting("mtg_a", "Monday", "", ""))}

	artifact, err := Run(context.Background(), fixedRun(Request{
		Workflow: workflow,
		Contexts: bundles,
		Provider: testProvider("```markdown\n# Answer\n\nInner ``` stays.\n```"),
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if artifact.Body != "# Answer\n\nInner ``` stays.\n" {
		t.Errorf("body = %q", artifact.Body)
	}
}

// The seam's whole point: one completion call, and nothing else reached for.
func TestRunMakesExactlyOneCompletionCall(t *testing.T) {
	provider := testProvider("answer")
	_, err := Run(context.Background(), fixedRun(Request{
		Workflow: testWorkflow(t, "Answer.\n\n{{TEMPLATE}}", "# Answer"),
		Contexts: []meetingcontext.Bundle{
			testBundle(meeting("mtg_a", "Monday", "", "")),
			testBundle(meeting("mtg_b", "Tuesday", "", "")),
		},
		Provider: provider,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("completion calls = %d, want 1", provider.calls)
	}
}

func TestNewArtifactIDIsPrefixedAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, err := NewArtifactID()
		if err != nil {
			t.Fatalf("NewArtifactID: %v", err)
		}
		if !strings.HasPrefix(id, "ins_") || len(id) != len("ins_")+16 {
			t.Fatalf("id = %q, want ins_ and sixteen hex characters", id)
		}
		if seen[id] {
			t.Fatalf("id %q was generated twice", id)
		}
		seen[id] = true
	}
}
