package insight

import (
	"encoding/json"
	"strings"
	"testing"
)

func succeededArtifact() Artifact {
	return Artifact{
		Record: Record{
			Version:       RecordVersion,
			ArtifactID:    "ins_0123456789abcdef",
			Status:        StatusSucceeded,
			Workflow:      WorkflowRef{ID: "summarise", Version: "v0", SHA256: "abc123"},
			Provider:      ProviderRef{Kind: "openai-compatible", BaseURL: "http://model.invalid/v1", Model: "qwen2.5:7b"},
			StartedAtUTC:  "2026-09-03T09:30:00Z",
			FinishedAtUTC: "2026-09-03T09:30:12Z",
			Context: ContextRef{
				Version: "cassini.meetings.context.v1",
				SHA256:  "def456",
				Bundles: 2,
				Meetings: []MeetingRef{
					{ID: "mtg_a", Title: "Monday", RoomID: "rm_one", RoomName: "Planning"},
					{ID: "mtg_b", Title: "Tuesday"},
				},
			},
		},
		Body: "# Answer\n\nThey agreed.\n",
	}
}

// The document has to be able to answer, on its own, which meetings, which
// prompt version and which model produced it.
func TestRenderMarkdownWritesTheProvenance(t *testing.T) {
	document, err := RenderMarkdown(succeededArtifact())
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}

	want := `---
version: "cassini.insight.record.v1"
artifactId: "ins_0123456789abcdef"
status: "succeeded"
startedAtUtc: "2026-09-03T09:30:00Z"
finishedAtUtc: "2026-09-03T09:30:12Z"
workflow:
  id: "summarise"
  version: "v0"
  sha256: "abc123"
provider:
  kind: "openai-compatible"
  baseUrl: "http://model.invalid/v1"
  model: "qwen2.5:7b"
context:
  version: "cassini.meetings.context.v1"
  sha256: "def456"
  bundles: 2
  timestamps: false
  meetings:
    - id: "mtg_a"
      title: "Monday"
      roomId: "rm_one"
      roomName: "Planning"
    - id: "mtg_b"
      title: "Tuesday"
---

# Answer

They agreed.
`
	if document != want {
		t.Errorf("document =\n%s\nwant\n%s", document, want)
	}
}

// A record's strings come from meeting titles, room names and model ids, none
// of which this package chooses. Always quoting is what keeps a title of "yes"
// or a room called "12:30" from changing what the frontmatter means.
func TestFrontmatterQuotesEveryString(t *testing.T) {
	artifact := succeededArtifact()
	artifact.Record.Context.Meetings = []MeetingRef{
		{ID: "mtg_a", Title: `yes: "the 12:30 - sync"`, RoomName: "*starred*\nsecond line"},
	}
	document, err := RenderMarkdown(artifact)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(document, `      title: "yes: \"the 12:30 - sync\""`) {
		t.Errorf("a title with a colon and quotes was not escaped:\n%s", document)
	}
	if !strings.Contains(document, `      roomName: "*starred*\nsecond line"`) {
		t.Errorf("a room name with a newline was not escaped:\n%s", document)
	}
	// Whatever the values were, the frontmatter is still one block bounded by
	// two rules and nothing has escaped it.
	if !strings.HasPrefix(document, "---\n") || strings.Count(document, "\n---\n") != 1 {
		t.Errorf("the frontmatter block is not intact:\n%s", document)
	}
}

// A failed run has a record and no document. Writing one anyway would put a
// file on disk that says nothing about why it is empty.
func TestRenderMarkdownRefusesWhatIsNotAnAnswer(t *testing.T) {
	failed := succeededArtifact()
	failed.Record.Status = StatusFailed
	failed.Body = ""
	if _, err := RenderMarkdown(failed); err == nil {
		t.Error("RenderMarkdown wrote a document for a failed run")
	}

	empty := succeededArtifact()
	empty.Body = "   \n"
	if _, err := RenderMarkdown(empty); err == nil {
		t.Error("RenderMarkdown wrote a document with no body")
	}
}

// The .md and the .json describe one run, so a harness handed the pair is never
// told two different stories.
func TestEncodeRecordJSONMatchesTheFrontmatter(t *testing.T) {
	artifact := succeededArtifact()
	buf := &strings.Builder{}
	if err := EncodeRecordJSON(buf, artifact.Record); err != nil {
		t.Fatalf("EncodeRecordJSON: %v", err)
	}
	var back Record
	if err := json.Unmarshal([]byte(buf.String()), &back); err != nil {
		t.Fatalf("decode the record: %v", err)
	}
	if back.ArtifactID != artifact.Record.ArtifactID || back.Workflow != artifact.Record.Workflow || back.Provider != artifact.Record.Provider {
		t.Errorf("record round-tripped as %+v", back)
	}
	if len(back.Context.Meetings) != 2 || back.Context.Meetings[0].RoomName != "Planning" {
		t.Errorf("meetings round-tripped as %+v", back.Context.Meetings)
	}
	if !strings.Contains(buf.String(), "\n  \"artifactId\"") {
		t.Errorf("the record is not indented for reading by hand:\n%s", buf.String())
	}
}

// A failed run's record is written too — it is the only durable statement of
// what was attempted.
func TestEncodeRecordJSONCarriesAFailure(t *testing.T) {
	record := succeededArtifact().Record
	record.Status = StatusFailed
	record.Reason = ReasonProviderRefused
	record.Error = "API returned 401: no key"

	buf := &strings.Builder{}
	if err := EncodeRecordJSON(buf, record); err != nil {
		t.Fatalf("EncodeRecordJSON: %v", err)
	}
	for _, want := range []string{`"status": "failed"`, `"reason": "provider-refused"`, `"error": "API returned 401: no key"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("record is missing %s:\n%s", want, buf.String())
		}
	}
}
