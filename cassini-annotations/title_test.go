package annotations

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var titleStamp = Stamp{
	ActorKind:   AnnotationActorPerson,
	ActorID:     "alice",
	OperationID: "op_title",
	CreatedAt:   "2026-10-20T09:12:04Z",
}

func applyTitleOps(t *testing.T, current *Annotations, raw string) Outcome {
	t.Helper()
	batch, err := ParseBatch([]byte(raw))
	if err != nil {
		t.Fatalf("ParseBatch(%s) error = %v", raw, err)
	}
	outcome, err := ApplyBatch(current, batch, 60000, titleStamp)
	if err != nil {
		t.Fatalf("ApplyBatch(%s) error = %v", raw, err)
	}
	return outcome
}

func TestSetTitleOpSetsClearsAndIsIdempotent(t *testing.T) {
	outcome := applyTitleOps(t, nil, `{"ops":[{"op":"set-title","title":"  Q4 hiring sync  "}]}`)
	if !outcome.Changed {
		t.Fatal("setting a title on an untitled meeting must change the document")
	}
	meeting := outcome.Doc.Meeting
	if meeting == nil || meeting.Title != "Q4 hiring sync" {
		t.Fatalf("Meeting = %+v, want trimmed title", meeting)
	}
	if meeting.SetAtUTC != titleStamp.CreatedAt || meeting.Actor.ID != "alice" || meeting.Actor.Kind != AnnotationActorPerson {
		t.Fatalf("Meeting stamp = %+v, want the batch's stamp", meeting)
	}

	// The same title again is a no-op: no change, and the original stamp stays.
	later := titleStamp
	later.CreatedAt = "2026-10-21T10:00:00Z"
	batch, err := ParseBatch([]byte(`{"ops":[{"op":"set-title","title":"Q4 hiring sync"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := ApplyBatch(outcome.Doc, batch, 60000, later)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Changed {
		t.Fatal("re-setting the same title must not change the document")
	}
	if repeat.Doc.Meeting.SetAtUTC != titleStamp.CreatedAt {
		t.Fatalf("SetAtUTC = %q; a no-op must keep the original stamp", repeat.Doc.Meeting.SetAtUTC)
	}

	// An empty title clears back to the default.
	cleared := applyTitleOps(t, outcome.Doc, `{"ops":[{"op":"set-title","title":"  "}]}`)
	if !cleared.Changed || cleared.Doc.Meeting != nil {
		t.Fatalf("clear: changed=%t meeting=%+v, want changed with no meeting member", cleared.Changed, cleared.Doc.Meeting)
	}
	// Clearing an already-untitled meeting changes nothing.
	noop := applyTitleOps(t, nil, `{"ops":[{"op":"set-title"}]}`)
	if noop.Changed {
		t.Fatal("clearing an untitled meeting must not change the document")
	}
}

func TestSetTitleOpRefusesInvalidTitlesAndForeignMembers(t *testing.T) {
	batch, err := ParseBatch([]byte(`{"ops":[{"op":"set-title","title":"bad\u0000title"}]}`))
	if err != nil {
		t.Fatalf("ParseBatch error = %v", err)
	}
	_, err = ApplyBatch(nil, batch, 60000, titleStamp)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != ExitInvalid {
		t.Fatalf("control characters: err = %v, want ExitInvalid", err)
	}

	long := strings.Repeat("x", MaxAnnotationTitleRunes+1)
	batch, err = ParseBatch([]byte(`{"ops":[{"op":"set-title","title":"` + long + `"}]}`))
	if err != nil {
		t.Fatalf("ParseBatch error = %v", err)
	}
	if _, err = ApplyBatch(nil, batch, 60000, titleStamp); !errors.As(err, &failure) || failure.Code != ExitInvalid {
		t.Fatalf("overlong title: err = %v, want ExitInvalid", err)
	}

	// A member of another op is refused, not ignored.
	if _, err := ParseBatch([]byte(`{"ops":[{"op":"set-title","title":"x","tagId":"tag_1"}]}`)); err == nil {
		t.Fatal("a set-title carrying tagId must be refused")
	}
}

func TestCloneCopiesMeeting(t *testing.T) {
	doc := &Annotations{Meeting: &AnnotationMeeting{Title: "Original", SetAtUTC: titleStamp.CreatedAt, Actor: AnnotationActor{Kind: "person", ID: "alice"}}}
	clone := Clone(doc)
	clone.Meeting.Title = "Changed"
	if doc.Meeting.Title != "Original" {
		t.Fatal("Clone must not share the meeting member with its source")
	}
}

func TestValidateAnnotationsChecksMeeting(t *testing.T) {
	base := func() *Annotations {
		return &Annotations{
			Format: AnnotationsFormatV1, Revision: 1,
			AudioOpusSHA256: strings.Repeat("ab", 32),
			TagNamespace:    "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
			Tags:            []AnnotationTag{}, Items: []AnnotationItem{},
			Meeting: &AnnotationMeeting{Title: "Q4 hiring sync", SetAtUTC: "2026-10-20T09:12:04Z", Actor: AnnotationActor{Kind: "person", ID: "alice"}},
		}
	}
	if err := ValidateAnnotations(base(), 60000); err != nil {
		t.Fatalf("a valid meeting member must validate: %v", err)
	}
	bad := base()
	bad.Meeting.SetAtUTC = "yesterday"
	if err := ValidateAnnotations(bad, 60000); err == nil {
		t.Fatal("a malformed setAtUtc must be refused")
	}
	bad = base()
	bad.Meeting.Actor = AnnotationActor{}
	if err := ValidateAnnotations(bad, 60000); err == nil {
		t.Fatal("a missing actor must be refused")
	}
	bad = base()
	bad.Meeting.Title = " padded "
	if err := ValidateAnnotations(bad, 60000); err == nil {
		t.Fatal("an untrimmed title must be refused")
	}
}

func TestUserTitleReadsTolerantly(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"absent member", "", ""},
		{"null member", "null", ""},
		{"no meeting", `{"format":"cassini.annotations.v1","revision":1,"tags":[],"items":[]}`, ""},
		{"unknown format", `{"format":"cassini.annotations.v2","meeting":{"title":"X"}}`, ""},
		{"malformed", `{"format":`, ""},
		{"titled", `{"format":"cassini.annotations.v1","revision":1,"tags":[],"items":[],"meeting":{"title":" Q4 hiring sync "}}`, "Q4 hiring sync"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UserTitle(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("UserTitle(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
