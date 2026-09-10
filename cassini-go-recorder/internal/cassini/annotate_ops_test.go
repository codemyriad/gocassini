package cassini

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// The op semantics, worked out in memory — no ffmpeg, no files. The round trips
// through a real .opus live in annotate_test.go.

const annotateTestDurationMS = 60_000

func annotateMS(v int64) *int64 { return &v }

func annotateTestStamp() annotateStamp {
	return annotateStamp{ActorKind: "person", ActorID: "alice", OperationID: "op_batch", CreatedAt: "2026-09-10T11:23:54Z"}
}

// annotateTestDoc is a file that already carries three marks under two tags,
// from two earlier batches.
func annotateTestDoc() *portable.Annotations {
	earlier := func(id, tagID, op string, target portable.AnnotationTarget) portable.AnnotationItem {
		return portable.AnnotationItem{ID: id, TagID: tagID, Target: target, CreatedAtUTC: "2026-09-09T10:00:00Z",
			Actor: portable.AnnotationActor{Kind: "person", ID: "bob"}, OperationID: op}
	}
	meeting := portable.AnnotationTarget{Kind: portable.AnnotationTargetMeeting}
	span := func(start, end int64) portable.AnnotationTarget {
		return portable.AnnotationTarget{Kind: portable.AnnotationTargetTimeRange, StartMS: annotateMS(start), EndMS: annotateMS(end)}
	}
	return &portable.Annotations{
		Format:          portable.AnnotationsFormatV1,
		Revision:        3,
		AudioOpusSHA256: strings.Repeat("a", 64),
		TagNamespace:    "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
		Tags:            []portable.AnnotationTag{{ID: "tag_budget", Label: "Budget"}, {ID: "tag_hiring", Label: "hiring"}},
		Items: []portable.AnnotationItem{
			earlier("mk_1", "tag_hiring", "op_old", meeting),
			earlier("mk_2", "tag_budget", "op_old", span(1000, 2000)),
			earlier("mk_3", "tag_hiring", "op_other", span(5000, 6000)),
		},
	}
}

func applyTestOps(t *testing.T, doc *portable.Annotations, raw string) annotateOpsOutcome {
	t.Helper()
	ops, err := parseAnnotateOps([]byte(raw))
	if err != nil {
		t.Fatalf("parse ops: %v", err)
	}
	outcome, err := applyAnnotationOps(doc, ops, annotateTestDurationMS, annotateTestStamp())
	if err != nil {
		t.Fatalf("apply ops: %v", err)
	}
	return outcome
}

func annotateTagIDs(doc *portable.Annotations) []string {
	ids := make([]string, 0, len(doc.Tags))
	for _, tag := range doc.Tags {
		ids = append(ids, tag.ID)
	}
	return ids
}

func annotateItemIDs(doc *portable.Annotations) []string {
	ids := make([]string, 0, len(doc.Items))
	for _, item := range doc.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestAnnotationOpsMarkFindsTheTagByIDThenByLabel(t *testing.T) {
	outcome := applyTestOps(t, annotateTestDoc(), `{"ops":[
		{"op":"mark","tag":{"id":"tag_hiring","label":"something else"},"target":{"kind":"time-range","startMs":100,"endMs":200}},
		{"op":"mark","tag":{"label":"  HIRING  "},"target":{"kind":"time-range","startMs":300,"endMs":400}},
		{"op":"mark","tag":{"id":"tag_from_elsewhere","label":"budget"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"id":"tag_roadmap","label":"Roadmap"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"label":"Fresh"},"target":{"kind":"meeting"}}
	]}`)
	doc := outcome.Doc
	if len(outcome.Added) != 5 {
		t.Fatalf("added %v, want five new marks", outcome.Added)
	}
	tagOf := map[string]string{}
	for _, item := range doc.Items {
		tagOf[item.ID] = item.TagID
	}
	var tagsAdded []string
	for _, id := range outcome.Added {
		tagsAdded = append(tagsAdded, tagOf[id])
	}
	// Canonical order puts the three meeting targets first (by id, which is
	// random), so compare the set of tags the new marks landed on.
	for _, want := range []string{"tag_hiring", "tag_budget", "tag_roadmap"} {
		if !slices.Contains(tagsAdded, want) {
			t.Errorf("no new mark landed on %s; new marks are on %v", want, tagsAdded)
		}
	}
	if n := strings.Count(strings.Join(tagsAdded, ","), "tag_hiring"); n != 2 {
		t.Errorf("%d new marks on tag_hiring, want 2 (one by id, one by label)", n)
	}
	if slices.Contains(annotateTagIDs(doc), "tag_from_elsewhere") {
		t.Error("a mark whose label matches a tag in this file defined a second tag instead of using it")
	}
	for _, tag := range doc.Tags {
		switch tag.ID {
		case "tag_hiring":
			if tag.Label != "hiring" {
				t.Errorf("a mark by id renamed the tag to %q; a mark is not a relabel", tag.Label)
			}
		case "tag_roadmap":
			if tag.Label != "Roadmap" {
				t.Errorf("tag_roadmap label = %q", tag.Label)
			}
		case "tag_budget":
		default:
			if tag.Label != "Fresh" || !regexp.MustCompile(`^tag_[a-z2-7]{26}$`).MatchString(tag.ID) {
				t.Errorf("unexpected tag %+v; a new label with no id must get a minted random id", tag)
			}
		}
	}
	if len(doc.Tags) != 4 {
		t.Errorf("tags = %+v, want the two existing plus Roadmap and Fresh", doc.Tags)
	}
	for _, item := range doc.Items {
		if !slices.Contains(outcome.Added, item.ID) {
			continue
		}
		if item.OperationID != "op_batch" || item.Actor.ID != "alice" || item.Actor.Kind != "person" || item.CreatedAtUTC != "2026-09-10T11:23:54Z" {
			t.Errorf("new mark not stamped with the batch: %+v", item)
		}
	}
}

func TestAnnotationOpsMarkWithAnUndefinedIDAndNoLabelIsInvalid(t *testing.T) {
	ops, err := parseAnnotateOps([]byte(`{"ops":[{"op":"mark","tag":{"id":"tag_unknown"},"target":{"kind":"meeting"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyAnnotationOps(annotateTestDoc(), ops, annotateTestDurationMS, annotateTestStamp())
	if annotateExitCodeFor(err) != annotateExitInvalid || !strings.Contains(err.Error(), "not defined in this file") {
		t.Fatalf("want exit 4 naming the undefined tag, got %v", err)
	}
}

func TestAnnotationOpsAnIdenticalMarkIsANoOp(t *testing.T) {
	// Both of these marks already exist. A retried request must not add them
	// again — and a batch that adds nothing is not a change.
	outcome := applyTestOps(t, annotateTestDoc(), `{"ops":[
		{"op":"mark","tag":{"id":"tag_hiring"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"label":"Hiring"},"target":{"kind":"time-range","startMs":5000,"endMs":6000}}
	]}`)
	if outcome.Changed || len(outcome.Added) != 0 || len(outcome.Doc.Items) != 3 {
		t.Fatalf("re-marking existing marks changed the document: changed=%v added=%v items=%d",
			outcome.Changed, outcome.Added, len(outcome.Doc.Items))
	}

	// And within one batch.
	outcome = applyTestOps(t, nil, `{"ops":[
		{"op":"mark","tag":{"label":"new"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"label":"NEW"},"target":{"kind":"meeting"}}
	]}`)
	if len(outcome.Doc.Items) != 1 || len(outcome.Doc.Tags) != 1 {
		t.Fatalf("the same mark twice in one batch gave %d items and %d tags, want one of each", len(outcome.Doc.Items), len(outcome.Doc.Tags))
	}
}

func TestAnnotationOpsRemovals(t *testing.T) {
	cases := []struct {
		name         string
		ops          string
		wantItems    []string
		wantTags     []string
		wantNotFound []string
	}{
		{
			name:      "unmark drops the item, and the tag it leaves unused",
			ops:       `{"ops":[{"op":"unmark","itemId":"mk_2"}]}`,
			wantItems: []string{"mk_1", "mk_3"}, wantTags: []string{"tag_hiring"}, wantNotFound: []string{},
		},
		{
			name:      "unmark of an unknown id is reported, not refused",
			ops:       `{"ops":[{"op":"unmark","itemId":"mk_nope"},{"op":"unmark","itemId":"mk_nope"}]}`,
			wantItems: []string{"mk_1", "mk_2", "mk_3"}, wantTags: []string{"tag_budget", "tag_hiring"}, wantNotFound: []string{"mk_nope"},
		},
		{
			name:      "unmark-tag with a target removes only that mark",
			ops:       `{"ops":[{"op":"unmark-tag","tagId":"tag_hiring","target":{"kind":"time-range","startMs":5000,"endMs":6000}}]}`,
			wantItems: []string{"mk_1", "mk_2"}, wantTags: []string{"tag_budget", "tag_hiring"}, wantNotFound: []string{},
		},
		{
			name:      "unmark-tag without a target removes every mark of the tag",
			ops:       `{"ops":[{"op":"unmark-tag","tagId":"tag_hiring"}]}`,
			wantItems: []string{"mk_2"}, wantTags: []string{"tag_budget"}, wantNotFound: []string{},
		},
		{
			name:      "unmark-tag that matches nothing is reported",
			ops:       `{"ops":[{"op":"unmark-tag","tagId":"tag_hiring","target":{"kind":"time-range","startMs":1,"endMs":2}},{"op":"unmark-tag","tagId":"tag_nope"}]}`,
			wantItems: []string{"mk_1", "mk_2", "mk_3"}, wantTags: []string{"tag_budget", "tag_hiring"}, wantNotFound: []string{"tag_hiring", "tag_nope"},
		},
		{
			name:      "undo-operation removes everything one batch added",
			ops:       `{"ops":[{"op":"undo-operation","operationId":"op_old"}]}`,
			wantItems: []string{"mk_3"}, wantTags: []string{"tag_hiring"}, wantNotFound: []string{},
		},
		{
			name:      "undo-operation of an unknown batch is reported",
			ops:       `{"ops":[{"op":"undo-operation","operationId":"op_never"}]}`,
			wantItems: []string{"mk_1", "mk_2", "mk_3"}, wantTags: []string{"tag_budget", "tag_hiring"}, wantNotFound: []string{"op_never"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := applyTestOps(t, annotateTestDoc(), tc.ops)
			if got := annotateItemIDs(outcome.Doc); !slices.Equal(got, tc.wantItems) {
				t.Errorf("items = %v, want %v", got, tc.wantItems)
			}
			if got := annotateTagIDs(outcome.Doc); !slices.Equal(got, tc.wantTags) {
				t.Errorf("tags = %v, want %v", got, tc.wantTags)
			}
			if !slices.Equal(outcome.NotFound, tc.wantNotFound) {
				t.Errorf("notFound = %v, want %v", outcome.NotFound, tc.wantNotFound)
			}
			if outcome.Changed != (len(tc.wantItems) != 3) {
				t.Errorf("changed = %v", outcome.Changed)
			}
		})
	}
}

func TestAnnotationOpsRelabel(t *testing.T) {
	outcome := applyTestOps(t, annotateTestDoc(), `{"ops":[{"op":"relabel","tagId":"tag_budget","label":"  Money "}]}`)
	if outcome.Doc.Tags[1].ID != "tag_budget" || outcome.Doc.Tags[1].Label != "Money" || !outcome.Changed {
		t.Fatalf("relabel did not rename (trimmed, and re-sorted by the new label): %+v", outcome.Doc.Tags)
	}

	// Changing only the case of a tag's own label is not a collision.
	outcome = applyTestOps(t, annotateTestDoc(), `{"ops":[{"op":"relabel","tagId":"tag_hiring","label":"Hiring"}]}`)
	if outcome.Doc.Tags[1].Label != "Hiring" {
		t.Fatalf("relabel to a new case of the same word failed: %+v", outcome.Doc.Tags)
	}

	outcome = applyTestOps(t, annotateTestDoc(), `{"ops":[{"op":"relabel","tagId":"tag_nope","label":"x"}]}`)
	if outcome.Changed || !slices.Equal(outcome.NotFound, []string{"tag_nope"}) {
		t.Fatalf("relabel of an undefined tag: changed=%v notFound=%v", outcome.Changed, outcome.NotFound)
	}

	// Two tags with one name in a file would make a mark by label ambiguous.
	ops, err := parseAnnotateOps([]byte(`{"ops":[{"op":"relabel","tagId":"tag_hiring","label":"BUDGET"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyAnnotationOps(annotateTestDoc(), ops, annotateTestDurationMS, annotateTestStamp()); annotateExitCodeFor(err) != annotateExitInvalid || !strings.Contains(err.Error(), "already tag") {
		t.Fatalf("a relabel onto another tag's name must be refused with exit 4, got %v", err)
	}
}

func TestAnnotationOpsAddedAndRemovedAreNet(t *testing.T) {
	outcome := applyTestOps(t, annotateTestDoc(), `{"ops":[
		{"op":"mark","tag":{"id":"tag_temp","label":"temp"},"target":{"kind":"meeting"}},
		{"op":"unmark-tag","tagId":"tag_temp"},
		{"op":"unmark","itemId":"mk_3"},
		{"op":"mark","tag":{"id":"tag_hiring"},"target":{"kind":"time-range","startMs":5000,"endMs":6000}}
	]}`)
	if len(outcome.Added) != 1 || !slices.Equal(outcome.Removed, []string{"mk_3"}) {
		t.Fatalf("added=%v removed=%v; want the re-mark as one new id and mk_3 removed, the temp mark in neither", outcome.Added, outcome.Removed)
	}
	if slices.Contains(annotateTagIDs(outcome.Doc), "tag_temp") {
		t.Error("a tag added and emptied in one batch survived")
	}
}

func TestAnnotationOpsProduceCanonicalOrder(t *testing.T) {
	outcome := applyTestOps(t, nil, `{"ops":[
		{"op":"mark","tag":{"id":"tag_b","label":"beta"},"target":{"kind":"time-range","startMs":500,"endMs":600}},
		{"op":"mark","tag":{"id":"tag_a","label":"Alpha"},"target":{"kind":"time-range","startMs":100,"endMs":200}},
		{"op":"mark","tag":{"id":"tag_b"},"target":{"kind":"meeting"}}
	]}`)
	doc := outcome.Doc
	if got := annotateTagIDs(doc); !slices.Equal(got, []string{"tag_a", "tag_b"}) {
		t.Errorf("tags = %v, want ordered by label", got)
	}
	var kinds []string
	for _, item := range doc.Items {
		kind := item.Target.Kind
		if item.Target.StartMS != nil {
			kind += ":" + strings.Repeat("x", int(*item.Target.StartMS/100))
		}
		kinds = append(kinds, kind)
	}
	if want := []string{"meeting", "time-range:x", "time-range:xxxxx"}; !slices.Equal(kinds, want) {
		t.Errorf("items = %v, want the meeting target first, then by start", kinds)
	}
}

func TestApplyAnnotationOpsLeavesTheCurrentDocumentAlone(t *testing.T) {
	current := annotateTestDoc()
	applyTestOps(t, current, `{"ops":[{"op":"unmark","itemId":"mk_1"},{"op":"relabel","tagId":"tag_budget","label":"Money"}]}`)
	if len(current.Items) != 3 || current.Tags[0].Label != "Budget" {
		t.Fatalf("applying ops edited the document it was given: %+v", current)
	}
}

func TestAnnotationOpsRejectInvalidOps(t *testing.T) {
	mark := func(tag, target string) string {
		return `{"ops":[{"op":"mark","tag":` + tag + `,"target":` + target + `}]}`
	}
	meeting := `{"kind":"meeting"}`
	cases := []struct {
		name string
		ops  string
		want string
	}{
		{"meeting target with times", mark(`{"label":"x"}`, `{"kind":"meeting","startMs":0}`), "carries no startMs"},
		{"range without an end", mark(`{"label":"x"}`, `{"kind":"time-range","startMs":0}`), "needs both"},
		{"negative start", mark(`{"label":"x"}`, `{"kind":"time-range","startMs":-1,"endMs":10}`), "negative"},
		{"reversed range", mark(`{"label":"x"}`, `{"kind":"time-range","startMs":20,"endMs":10}`), "empty or reversed"},
		{"empty range", mark(`{"label":"x"}`, `{"kind":"time-range","startMs":10,"endMs":10}`), "empty or reversed"},
		{"past the end", mark(`{"label":"x"}`, `{"kind":"time-range","startMs":10,"endMs":60001}`), "past the end"},
		{"unknown target kind", mark(`{"label":"x"}`, `{"kind":"speaker"}`), "unknown kind"},
		{"blank label", mark(`{"label":"   "}`, meeting), "1-64 characters"},
		{"long label", mark(`{"label":"`+strings.Repeat("x", 65)+`"}`, meeting), "1-64 characters"},
		{"control character", mark(`{"label":"a\u0007b"}`, meeting), "control characters"},
		{"no tag", `{"ops":[{"op":"mark","target":{"kind":"meeting"}}]}`, "needs a tag"},
		{"no target", `{"ops":[{"op":"mark","tag":{"label":"x"}}]}`, "needs a target"},
		{"tag with neither id nor label", mark(`{}`, meeting), "needs an id or a label"},
		{"unmark without an id", `{"ops":[{"op":"unmark"}]}`, "needs an itemId"},
		{"unmark-tag without a tag", `{"ops":[{"op":"unmark-tag"}]}`, "needs a tagId"},
		{"unmark-tag with a bad target", `{"ops":[{"op":"unmark-tag","tagId":"tag_hiring","target":{"kind":"time-range","startMs":5}}]}`, "needs both"},
		{"undo without an operation", `{"ops":[{"op":"undo-operation"}]}`, "needs an operationId"},
		{"relabel without a label", `{"ops":[{"op":"relabel","tagId":"tag_hiring"}]}`, "needs a label"},
		{"relabel to a blank label", `{"ops":[{"op":"relabel","tagId":"tag_hiring","label":" "}]}`, "1-64 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops, err := parseAnnotateOps([]byte(tc.ops))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, err = applyAnnotationOps(annotateTestDoc(), ops, annotateTestDurationMS, annotateTestStamp())
			if annotateExitCodeFor(err) != annotateExitInvalid {
				t.Fatalf("exit = %d (%v), want %d", annotateExitCodeFor(err), err, annotateExitInvalid)
			}
			// The operator shows this message to the caller, so it must say
			// which op and what is wrong with it.
			if !strings.HasPrefix(err.Error(), "ops[0] (") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to name ops[0] and contain %q", err, tc.want)
			}
		})
	}
}

func TestParseAnnotateOpsIsStrict(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"not JSON", `not json`, `is not {"ops"`},
		{"an array", `[]`, `is not {"ops"`},
		{"no ops member", `{}`, `no "ops" array`},
		{"null ops", `{"ops":null}`, `no "ops" array`},
		{"an unknown envelope member", `{"ops":[],"expectRevision":1}`, "unknown field"},
		{"trailing content", `{"ops":[]} {}`, "after its closing brace"},
		{"a number for an op", `{"ops":[42]}`, "not a JSON object"},
		{"a null op", `{"ops":[null]}`, "not a JSON object"},
		{"an unknown op", `{"ops":[{"op":"tag"}]}`, `unknown op "tag"`},
		{"no op name", `{"ops":[{"itemId":"mk_1"}]}`, `unknown op ""`},
		{"a misspelt member", `{"ops":[{"op":"unmark","itemid":"mk_1"}]}`, `"itemid" is not a member`},
		{"another op's member", `{"ops":[{"op":"unmark","itemId":"mk_1","tagId":"tag_a"}]}`, `"tagId" is not a member`},
		{"an unknown tag member", `{"ops":[{"op":"mark","tag":{"label":"x","colour":"red"},"target":{"kind":"meeting"}}]}`, "unknown field"},
		{"a fractional time", `{"ops":[{"op":"mark","tag":{"label":"x"},"target":{"kind":"time-range","startMs":1.5,"endMs":3}}]}`, "cannot unmarshal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAnnotateOps([]byte(tc.raw))
			var failure *annotateFailure
			if !errors.As(err, &failure) || failure.code != annotateExitInvalid || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want exit 4 containing %q, got %v", tc.want, err)
			}
		})
	}
	ops, err := parseAnnotateOps([]byte(`{"ops":[]}`))
	if err != nil || len(ops) != 0 {
		t.Fatalf("an empty batch is valid: %v %v", ops, err)
	}
}
