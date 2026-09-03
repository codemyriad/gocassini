package meetingcontext

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Build is the only way to get a document that claims to be v1 and claims its
// prose is derived, so both claims are asserted rather than assumed.
func TestBuildStampsTheVersionAndTheProvenance(t *testing.T) {
	got := Build(BuildInput{ID: "M1", Title: "Daily Standup"})

	if got.Version != Version {
		t.Errorf("Version = %q, want %q", got.Version, Version)
	}
	if got.TranscriptSource != TranscriptSourceDerived {
		t.Errorf("TranscriptSource = %q, want %q", got.TranscriptSource, TranscriptSourceDerived)
	}
}

// A meeting with no transcribed speech carries an empty array, not null: the
// two read differently to a consumer, and "nothing was said" is the first.
func TestBuildNeverLeavesSegmentsNil(t *testing.T) {
	got := Build(BuildInput{ID: "M1"})
	if got.Segments == nil {
		t.Fatal("Segments is nil")
	}

	var out bytes.Buffer
	if err := EncodeJSON(&out, Bundle{Meetings: []MeetingContext{got}}); err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	if !strings.Contains(out.String(), `"segments": []`) {
		t.Errorf("expected an empty segments array:\n%s", out.String())
	}
}

func TestEncodeJSONRefusesAnEmptyBundle(t *testing.T) {
	if err := EncodeJSON(&bytes.Buffer{}, Bundle{}); err == nil {
		t.Error("encoding a bundle with no meetings should fail")
	}
}

// The round trip is what the insight run seam depends on: a bundle written to
// disk by the CLI and read back by another package must be the same bundle,
// for one meeting and for many.
func TestJSONRoundTrip(t *testing.T) {
	for _, count := range []int{1, 3} {
		bundle := Bundle{}
		for i := 0; i < count; i++ {
			bundle.Meetings = append(bundle.Meetings, Build(BuildInput{
				ID:       string(rune('A' + i)),
				Title:    "Meeting",
				Speakers: []Speaker{{ID: "spk1", Label: "Erlich"}},
				Segments: []Segment{{Speaker: "spk1", SpeakerLabel: "Erlich", StartMS: 5_000, EndMS: 6_000, Text: "we should ship it"}},
				Summary:  Summary{Present: true, Format: "markdown", Model: "m", Markdown: "# Summary\n"},
			}))
		}

		var out bytes.Buffer
		if err := EncodeJSON(&out, bundle); err != nil {
			t.Fatalf("%d meetings: EncodeJSON: %v", count, err)
		}
		got, err := DecodeJSON(out.Bytes())
		if err != nil {
			t.Fatalf("%d meetings: DecodeJSON: %v\n%s", count, err, out.String())
		}
		if !reflect.DeepEqual(got, bundle) {
			t.Errorf("%d meetings did not round-trip:\ngot  %+v\nwant %+v", count, got, bundle)
		}
	}
}

// The many-meeting form is additive: a reader that knows v1 finds the version
// it knows at the top, and finds a complete v1 document at every element,
// rather than a familiar-looking shape that describes only the first meeting.
func TestManyMeetingsNestCompleteV1DocumentsUnderAVersionedContainer(t *testing.T) {
	bundle := Bundle{Meetings: []MeetingContext{
		Build(BuildInput{ID: "A", Title: "First"}),
		Build(BuildInput{ID: "B", Title: "Second"}),
	}}

	var out bytes.Buffer
	if err := EncodeJSON(&out, bundle); err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}

	var document struct {
		Version  string            `json:"version"`
		Meeting  json.RawMessage   `json:"meeting"`
		Meetings []json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("parse: %v\n%s", err, out.String())
	}
	if document.Version != Version {
		t.Errorf("version = %q, want %q", document.Version, Version)
	}
	// The container must not also pretend to be one of its meetings — that is
	// the shape that would let a v1 reader silently answer from the first one.
	if len(document.Meeting) != 0 {
		t.Errorf("a many-meeting document must carry no top-level meeting:\n%s", out.String())
	}
	if len(document.Meetings) != 2 {
		t.Fatalf("got %d meetings, want 2", len(document.Meetings))
	}
	for i, raw := range document.Meetings {
		one, err := DecodeJSON(raw)
		if err != nil {
			t.Errorf("meeting %d is not a valid v1 document on its own: %v", i, err)
			continue
		}
		if len(one.Meetings) != 1 {
			t.Errorf("meeting %d decoded to %d meetings", i, len(one.Meetings))
		}
	}
}

func TestDecodeJSONRejectsAVersionItDoesNotKnow(t *testing.T) {
	// Decoding what it can from an unknown version is how a consumer ends up
	// answering confidently out of a shape it does not understand.
	cases := map[string]string{
		"a later single document":            `{"version":"cassini.meetings.context.v2","meeting":{"id":"A"},"segments":[]}`,
		"a later container":                  `{"version":"cassini.meetings.context.v2","meetings":[]}`,
		"a container of later ones":          `{"version":"cassini.meetings.context.v1","meetings":[{"version":"cassini.meetings.context.v2","meeting":{"id":"A"}}]}`,
		"a document with no version":         `{"meeting":{"id":"A"},"segments":[]}`,
		"an empty container":                 `{"version":"cassini.meetings.context.v1","meetings":[]}`,
		"something that is not JSON":         `not json at all`,
		"a JSON array rather than an object": `["cassini.meetings.context.v1"]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJSON([]byte(body)); err == nil {
				t.Errorf("DecodeJSON(%s) should have failed", body)
			}
		})
	}
}

// The schema's oneOf has `additionalProperties: false` on both branches, so a
// document carrying keys from both forms is not "a container with an extra
// field" — it is neither form. Decoding it anyway means dropping a meeting the
// caller asked about, or answering out of a meeting with no id and no speech,
// and both are silent.
func TestDecodeJSONRejectsADocumentThatIsNeitherForm(t *testing.T) {
	const meeting = `{"version":"cassini.meetings.context.v1","meeting":{"id":"A"},"segments":[]}`
	cases := map[string]string{
		"both forms at once":         `{"version":"cassini.meetings.context.v1","meeting":{"id":"A"},"segments":[],"meetings":[` + meeting + `]}`,
		"a container of a container": `{"version":"cassini.meetings.context.v1","meetings":[{"version":"cassini.meetings.context.v1","meetings":[` + meeting + `]}]}`,
		"neither form":               `{"version":"cassini.meetings.context.v1"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeJSON([]byte(body))
			if err == nil {
				t.Fatalf("DecodeJSON(%s) returned %d meetings, want an error", body, len(got.Meetings))
			}
		})
	}
}
