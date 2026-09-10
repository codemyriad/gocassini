package portable

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
)

const testAudioSHA = "8e1f7499c6d5fba88c3bd9b69ecd3de1b07ae0cff65152c942c5e99062d01cbc"

func ptr(v int64) *int64 { return &v }

func validAnnotations() *Annotations {
	return &Annotations{
		Format:          AnnotationsFormatV1,
		Revision:        2,
		AudioOpusSHA256: testAudioSHA,
		TagNamespace:    "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
		Tags:            []AnnotationTag{{ID: "tag_a", Label: "hiring"}, {ID: "tag_b", Label: "budget"}},
		Items: []AnnotationItem{
			{ID: "mk_1", TagID: "tag_a", Target: AnnotationTarget{Kind: AnnotationTargetMeeting},
				CreatedAtUTC: "2026-09-10T11:23:54Z", Actor: AnnotationActor{Kind: AnnotationActorPerson, ID: "alice"}, OperationID: "op_1"},
			{ID: "mk_2", TagID: "tag_b", Target: AnnotationTarget{Kind: AnnotationTargetTimeRange, StartMS: ptr(869000), EndMS: ptr(884000)},
				CreatedAtUTC: "2026-09-10T11:24:10Z", Actor: AnnotationActor{Kind: AnnotationActorAgent, ID: "alice"}, OperationID: "op_2"},
		},
	}
}

func TestValidateAnnotationsAcceptsAValidDocument(t *testing.T) {
	if err := ValidateAnnotations(validAnnotations(), 3_600_000); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
}

func TestValidateAnnotationsRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(a *Annotations)
		want   string
	}{
		{"wrong format", func(a *Annotations) { a.Format = "cassini.annotations.v2" }, "annotations.format"},
		{"zero revision", func(a *Annotations) { a.Revision = 0 }, "annotations.revision"},
		{"bad audio digest", func(a *Annotations) { a.AudioOpusSHA256 = "ABC" }, "audioOpusSha256"},
		{"bad namespace", func(a *Annotations) { a.TagNamespace = "urn:uuid:NOT-A-UUID" }, "tagNamespace"},
		{"duplicate tag id", func(a *Annotations) { a.Tags[1].ID = "tag_a" }, "defined twice"},
		{"untrimmed label", func(a *Annotations) { a.Tags[0].Label = " hiring" }, "whitespace"},
		{"empty label", func(a *Annotations) { a.Tags[0].Label = "" }, "1-64 characters"},
		{"long label", func(a *Annotations) { a.Tags[0].Label = strings.Repeat("x", 65) }, "1-64 characters"},
		{"control character", func(a *Annotations) { a.Tags[0].Label = "a\x07b" }, "control characters"},
		{"duplicate item id", func(a *Annotations) { a.Items[1].ID = "mk_1" }, "appears twice"},
		{"unknown tag", func(a *Annotations) { a.Items[0].TagID = "tag_zzz" }, "names no tag"},
		{"empty range", func(a *Annotations) { a.Items[1].Target.EndMS = ptr(869000) }, "empty or reversed"},
		{"negative start", func(a *Annotations) { a.Items[1].Target.StartMS = ptr(-1) }, "negative"},
		{"past the end", func(a *Annotations) { a.Items[1].Target.EndMS = ptr(3_600_001) }, "past the end"},
		{"range missing end", func(a *Annotations) { a.Items[1].Target.EndMS = nil }, "needs both"},
		{"meeting with times", func(a *Annotations) { a.Items[0].Target.StartMS = ptr(0) }, "carries no startMs"},
		{"unknown kind", func(a *Annotations) { a.Items[0].Target.Kind = "speaker" }, "unknown kind"},
		{"local time", func(a *Annotations) { a.Items[0].CreatedAtUTC = "2026-09-10T11:23:54+02:00" }, "UTC"},
		{"empty actor", func(a *Annotations) { a.Items[0].Actor.ID = "" }, "actor.id"},
		{"bad operation id", func(a *Annotations) { a.Items[0].OperationID = "" }, "operationId"},
		{"too many items", func(a *Annotations) {
			a.Items = make([]AnnotationItem, MaxAnnotationItems+1)
		}, "exceeds the limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validAnnotations()
			tc.mutate(a)
			err := ValidateAnnotations(a, 3_600_000)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateAnnotationsNeedsADurationForRanges(t *testing.T) {
	if err := ValidateAnnotations(validAnnotations(), 0); err == nil || !strings.Contains(err.Error(), "duration is unknown") {
		t.Fatalf("want a duration error, got %v", err)
	}
	onlyMeeting := validAnnotations()
	onlyMeeting.Items = onlyMeeting.Items[:1]
	if err := ValidateAnnotations(onlyMeeting, 0); err != nil {
		t.Fatalf("a meeting-only document needs no duration: %v", err)
	}
}

func TestParseAnnotationsIsTolerant(t *testing.T) {
	for _, raw := range []string{"", "  ", "null"} {
		got, err := ParseAnnotations(json.RawMessage(raw))
		if got != nil || err != nil {
			t.Fatalf("%q: want (nil, nil), got (%v, %v)", raw, got, err)
		}
	}
	_, err := ParseAnnotations(json.RawMessage(`{"format":"cassini.annotations.v9","anything":true}`))
	if !errors.Is(err, ErrAnnotationsFormatUnsupported) {
		t.Fatalf("an unknown format must answer ErrAnnotationsFormatUnsupported, got %v", err)
	}
	raw, _ := json.Marshal(validAnnotations())
	got, err := ParseAnnotations(raw)
	if err != nil || got == nil || len(got.Items) != 2 {
		t.Fatalf("v1 did not parse: %v %+v", err, got)
	}
}

func TestResolvedComparesTheBinding(t *testing.T) {
	a := validAnnotations()
	if !a.Resolved(strings.ToUpper(testAudioSHA)) {
		t.Fatal("the same digest must resolve, case-insensitively")
	}
	if a.Resolved(strings.Repeat("0", 64)) {
		t.Fatal("a different digest must not resolve")
	}
	var none *Annotations
	if none.Resolved(testAudioSHA) {
		t.Fatal("nil must not resolve")
	}
}

func TestCanonicalizeOrdersTagsAndItems(t *testing.T) {
	a := validAnnotations()
	a.Items = append(a.Items, AnnotationItem{ID: "mk_0", TagID: "tag_b",
		Target: AnnotationTarget{Kind: AnnotationTargetTimeRange, StartMS: ptr(100), EndMS: ptr(200)}})
	a.Items[0], a.Items[1] = a.Items[1], a.Items[0]
	a.Canonicalize()
	if a.Tags[0].Label != "budget" || a.Tags[1].Label != "hiring" {
		t.Fatalf("tags not ordered by label: %+v", a.Tags)
	}
	got := []string{a.Items[0].ID, a.Items[1].ID, a.Items[2].ID}
	want := []string{"mk_1", "mk_0", "mk_2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items: want %v (meeting first, then by start), got %v", want, got)
		}
	}
}

func TestAnnotationIDsAreRandomAndWellFormed(t *testing.T) {
	re := regexp.MustCompile(`^(tag|mk|op)_[a-z2-7]{26}$`)
	seen := map[string]bool{}
	for _, mint := range []func() (string, error){NewAnnotationTagID, NewAnnotationItemID, NewAnnotationOperationID} {
		for i := 0; i < 50; i++ {
			id, err := mint()
			if err != nil || !re.MatchString(id) || !annotationIDRE.MatchString(id) {
				t.Fatalf("bad id %q (%v)", id, err)
			}
			if seen[id] {
				t.Fatalf("duplicate id %q", id)
			}
			seen[id] = true
		}
	}
}
