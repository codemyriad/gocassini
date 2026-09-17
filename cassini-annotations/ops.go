package annotations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// The ops `cassini annotate apply` takes, and the pure function that works a
// batch out in memory before the one rewrite.

const (
	annotateOpMark          = "mark"
	annotateOpUnmark        = "unmark"
	annotateOpUnmarkTag     = "unmark-tag"
	annotateOpUndoOperation = "undo-operation"
	annotateOpRelabel       = "relabel"
	annotateOpMergeTag      = "merge-tag"
)

// annotateOpFields is the members each op may carry besides "op". Another op's
// member is refused, not ignored: an unmark sent with a tagId meant unmark-tag.
var annotateOpFields = map[string][]string{
	annotateOpMark:          {"tag", "target"},
	annotateOpUnmark:        {"itemId"},
	annotateOpUnmarkTag:     {"tagId", "target"},
	annotateOpUndoOperation: {"operationId"},
	annotateOpRelabel:       {"tagId", "label"},
	annotateOpMergeTag:      {"tagId", "into"},
}

// Op is the union of every op's members.
type Op struct {
	Op          string            `json:"op"`
	Tag         *OpTag            `json:"tag"`
	Target      *AnnotationTarget `json:"target"`
	ItemID      string            `json:"itemId"`
	TagID       string            `json:"tagId"`
	OperationID string            `json:"operationId"`
	Label       *string           `json:"label"`
	Into        *OpTag            `json:"into"`
	Raw         json.RawMessage   // the op as sent, for the meetings client to forward
}

// OpTag names the tag a mark applies, or a merge moves marks to. An
// absent label means "the tag with this id"; an empty one is invalid.
type OpTag struct {
	ID    string  `json:"id"`
	Label *string `json:"label"`
}

type Stamp struct {
	ActorKind   string
	ActorID     string
	OperationID string
	CreatedAt   string
}

type Outcome struct {
	// Doc is the tags and items after the batch, canonical, unused tags dropped;
	// its format, revision, binding and namespace are the caller's to set.
	Doc      *Annotations
	Changed  bool
	Added    []string
	Removed  []string
	NotFound []string
}

// ParseOps decodes an ops document strictly. Unknown members are
// refused at every level: a misspelt "tagid" read as absent would turn a
// targeted removal into a removal of everything.
func ParseOps(raw []byte) ([]Op, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope struct {
		Ops []json.RawMessage `json:"ops"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fail(ExitInvalid, `the ops document is not {"ops":[...]}: %v`, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fail(ExitInvalid, "the ops document has content after its closing brace")
	}
	if envelope.Ops == nil {
		return nil, fail(ExitInvalid, `the ops document has no "ops" array`)
	}

	ops := make([]Op, 0, len(envelope.Ops))
	for i, rawOp := range envelope.Ops {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(rawOp, &members); err != nil || members == nil {
			return nil, fail(ExitInvalid, "ops[%d]: not a JSON object", i)
		}
		opDecoder := json.NewDecoder(bytes.NewReader(rawOp))
		opDecoder.DisallowUnknownFields()
		var op Op
		if err := opDecoder.Decode(&op); err != nil {
			return nil, fail(ExitInvalid, "ops[%d]: %v", i, err)
		}
		allowed, known := annotateOpFields[op.Op]
		if !known {
			return nil, fail(ExitInvalid,
				"ops[%d]: unknown op %q (want mark, unmark, unmark-tag, undo-operation, relabel or merge-tag)", i, op.Op)
		}
		// encoding/json matches names case-insensitively; this checks the spelling.
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if name != "op" && !slices.Contains(allowed, name) {
				return nil, fail(ExitInvalid, "ops[%d] (%s): %q is not a member of this op", i, op.Op, name)
			}
		}
		op.Raw = rawOp
		ops = append(ops, op)
	}
	return ops, nil
}

// ApplyOps works out a batch against current (nil when the file
// carries none) without touching it. durationMS bounds every time range an op
// names. An op the caller got wrong fails the whole batch with exit 4.
func ApplyOps(current *Annotations, ops []Op, durationMS int64, stamp Stamp) (Outcome, error) {
	before := Clone(current)
	before.Canonicalize()
	work := Clone(current)
	notFound := []string{}

	for i, op := range ops {
		var err error
		switch op.Op {
		case annotateOpMark:
			err = applyMarkOp(work, op, durationMS, stamp)
		case annotateOpUnmark:
			err = applyUnmarkOp(work, op, &notFound)
		case annotateOpUnmarkTag:
			err = applyUnmarkTagOp(work, op, durationMS, &notFound)
		case annotateOpUndoOperation:
			err = applyUndoOperationOp(work, op, &notFound)
		case annotateOpRelabel:
			err = applyRelabelOp(work, op, &notFound)
		case annotateOpMergeTag:
			err = applyMergeTagOp(work, op, &notFound)
		default:
			err = fail(ExitInvalid, "unknown op")
		}
		if err != nil {
			var failure *Failure
			if errors.As(err, &failure) {
				return Outcome{}, &Failure{Code: failure.Code, Err: fmt.Errorf("ops[%d] (%s): %w", i, op.Op, failure.Err)}
			}
			return Outcome{}, err
		}
	}

	// A tag with no marks left would otherwise stay in the vocabulary.
	dropUnusedAnnotationTags(work)
	work.Canonicalize()

	outcome := Outcome{
		Doc:      work,
		NotFound: uniqueStrings(notFound),
		Added:    []string{},
		Removed:  []string{},
	}
	// Canonical order is total (ids are unique), so equal content means equal slices.
	outcome.Changed = !reflect.DeepEqual(before.Tags, work.Tags) || !reflect.DeepEqual(before.Items, work.Items)

	// Net, from the two id sets rather than tallied per op.
	beforeIDs := make(map[string]bool, len(before.Items))
	for _, item := range before.Items {
		beforeIDs[item.ID] = true
	}
	afterIDs := make(map[string]bool, len(work.Items))
	for _, item := range work.Items {
		afterIDs[item.ID] = true
		if !beforeIDs[item.ID] {
			outcome.Added = append(outcome.Added, item.ID)
		}
	}
	for _, item := range before.Items {
		if !afterIDs[item.ID] {
			outcome.Removed = append(outcome.Removed, item.ID)
		}
	}
	return outcome, nil
}

// applyMarkOp finds the tag by id, else by label (a mark is not a rename), else
// defines it, then adds the item unless an identical one is already there —
// which is what makes a retry after a lost response safe. Mapping a label to
// one id across the archive is the operator's job, before it calls this.
func applyMarkOp(doc *Annotations, op Op, durationMS int64, stamp Stamp) error {
	if op.Tag == nil {
		return fail(ExitInvalid, `needs a tag: {"id"?, "label"?}`)
	}
	if op.Target == nil {
		return fail(ExitInvalid, "needs a target")
	}
	label, hasLabel, err := annotateOpLabel(op.Tag.Label, "tag.label")
	if err != nil {
		return err
	}
	if op.Tag.ID == "" && !hasLabel {
		return fail(ExitInvalid, "the tag needs an id or a label")
	}
	target := copyAnnotationTarget(*op.Target)
	if err := checkAnnotateTarget(target, durationMS); err != nil {
		return err
	}

	tagID := findAnnotationTag(doc, op.Tag.ID, label, hasLabel)
	if tagID == "" {
		if !hasLabel {
			return fail(ExitInvalid, "tag %q is not defined in this file, and the op carries no label to define it with", op.Tag.ID)
		}
		tagID = op.Tag.ID
		if tagID == "" {
			if tagID, err = NewAnnotationTagID(); err != nil {
				return err
			}
		}
		doc.Tags = append(doc.Tags, AnnotationTag{ID: tagID, Label: label})
	}

	for _, item := range doc.Items {
		if item.TagID == tagID && sameAnnotationTarget(item.Target, target) {
			return nil
		}
	}
	itemID, err := NewAnnotationItemID()
	if err != nil {
		return err
	}
	doc.Items = append(doc.Items, AnnotationItem{
		ID:           itemID,
		TagID:        tagID,
		Target:       target,
		CreatedAtUTC: stamp.CreatedAt,
		Actor:        AnnotationActor{Kind: stamp.ActorKind, ID: stamp.ActorID},
		OperationID:  stamp.OperationID,
	})
	return nil
}

func applyUnmarkOp(doc *Annotations, op Op, notFound *[]string) error {
	if op.ItemID == "" {
		return fail(ExitInvalid, "needs an itemId")
	}
	if removeAnnotationItems(doc, func(item AnnotationItem) bool { return item.ID == op.ItemID }) == 0 {
		*notFound = append(*notFound, op.ItemID)
	}
	return nil
}

func applyUnmarkTagOp(doc *Annotations, op Op, durationMS int64, notFound *[]string) error {
	if op.TagID == "" {
		return fail(ExitInvalid, "needs a tagId")
	}
	var target *AnnotationTarget
	if op.Target != nil {
		checked := copyAnnotationTarget(*op.Target)
		if err := checkAnnotateTarget(checked, durationMS); err != nil {
			return err
		}
		target = &checked
	}
	removed := removeAnnotationItems(doc, func(item AnnotationItem) bool {
		return item.TagID == op.TagID && (target == nil || sameAnnotationTarget(item.Target, *target))
	})
	if removed == 0 {
		*notFound = append(*notFound, op.TagID)
	}
	return nil
}

func applyUndoOperationOp(doc *Annotations, op Op, notFound *[]string) error {
	if op.OperationID == "" {
		return fail(ExitInvalid, "needs an operationId")
	}
	if removeAnnotationItems(doc, func(item AnnotationItem) bool { return item.OperationID == op.OperationID }) == 0 {
		*notFound = append(*notFound, op.OperationID)
	}
	return nil
}

// applyRelabelOp renames a tag in this file only. A label another tag here
// already has is refused, or a mark by label could not say which it meant.
func applyRelabelOp(doc *Annotations, op Op, notFound *[]string) error {
	if op.TagID == "" {
		return fail(ExitInvalid, "needs a tagId")
	}
	if op.Label == nil {
		return fail(ExitInvalid, "needs a label")
	}
	label, _, err := annotateOpLabel(op.Label, "label")
	if err != nil {
		return err
	}
	index := -1
	for i, tag := range doc.Tags {
		if tag.ID == op.TagID {
			index = i
			break
		}
	}
	if index < 0 {
		*notFound = append(*notFound, op.TagID)
		return nil
	}
	for _, tag := range doc.Tags {
		if tag.ID != op.TagID && strings.EqualFold(tag.Label, label) {
			return fail(ExitInvalid, "label %q is already tag %q's in this file", label, tag.ID)
		}
	}
	doc.Tags[index].Label = label
	return nil
}

// applyMergeTagOp moves every mark of tagId in this file to into, defining into
// here if the file lacks it. A mark whose target into already covers is dropped
// as a duplicate; the emptied source is dropped with the other unused tags.
func applyMergeTagOp(doc *Annotations, op Op, notFound *[]string) error {
	if op.TagID == "" {
		return fail(ExitInvalid, "needs a tagId")
	}
	if op.Into == nil || op.Into.ID == "" {
		return fail(ExitInvalid, `needs into: {"id", "label"}`)
	}
	label, hasLabel, err := annotateOpLabel(op.Into.Label, "into.label")
	if err != nil {
		return err
	}
	if !hasLabel {
		return fail(ExitInvalid, "into needs a label, to define the tag in files that lack it")
	}
	if op.Into.ID == op.TagID {
		return fail(ExitInvalid, "a tag cannot be merged into itself")
	}
	if !slices.ContainsFunc(doc.Tags, func(tag AnnotationTag) bool { return tag.ID == op.TagID }) {
		*notFound = append(*notFound, op.TagID)
		return nil
	}
	if findAnnotationTag(doc, op.Into.ID, "", false) == "" {
		for _, tag := range doc.Tags {
			if tag.ID != op.TagID && strings.EqualFold(tag.Label, label) {
				return fail(ExitInvalid, "label %q is already tag %q's in this file", label, tag.ID)
			}
		}
		doc.Tags = append(doc.Tags, AnnotationTag{ID: op.Into.ID, Label: label})
	}

	var covered []AnnotationTarget
	for _, item := range doc.Items {
		if item.TagID == op.Into.ID {
			covered = append(covered, item.Target)
		}
	}
	kept := make([]AnnotationItem, 0, len(doc.Items))
	for _, item := range doc.Items {
		if item.TagID == op.TagID {
			if slices.ContainsFunc(covered, func(target AnnotationTarget) bool { return sameAnnotationTarget(target, item.Target) }) {
				continue
			}
			item.TagID = op.Into.ID
			covered = append(covered, item.Target)
		}
		kept = append(kept, item)
	}
	doc.Items = kept
	return nil
}

// annotateOpLabel trims and checks a label from an op; absent answers ("", false, nil).
func annotateOpLabel(raw *string, member string) (string, bool, error) {
	if raw == nil {
		return "", false, nil
	}
	label := strings.TrimSpace(*raw)
	if err := ValidateAnnotationLabel(label); err != nil {
		return "", false, fail(ExitInvalid, "%s: %v", member, err)
	}
	return label, true, nil
}

// checkAnnotateTarget refuses a bad target with a message pointing at the op
// rather than at an index in the rewritten document.
func checkAnnotateTarget(target AnnotationTarget, durationMS int64) error {
	if err := ValidateAnnotationTarget(target, durationMS); err != nil {
		return fail(ExitInvalid, "target: %v", err)
	}
	return nil
}

// findAnnotationTag answers the tag a mark applies to, or "" when it must be
// defined: by id, else by label, case-insensitively.
func findAnnotationTag(doc *Annotations, id, label string, hasLabel bool) string {
	if id != "" {
		for _, tag := range doc.Tags {
			if tag.ID == id {
				return tag.ID
			}
		}
	}
	if hasLabel {
		for _, tag := range doc.Tags {
			if strings.EqualFold(tag.Label, label) {
				return tag.ID
			}
		}
	}
	return ""
}

func sameAnnotationTarget(a, b AnnotationTarget) bool {
	return a.Kind == b.Kind && sameInt64Pointer(a.StartMS, b.StartMS) && sameInt64Pointer(a.EndMS, b.EndMS)
}

func sameInt64Pointer(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func copyAnnotationTarget(target AnnotationTarget) AnnotationTarget {
	copied := AnnotationTarget{Kind: target.Kind}
	if target.StartMS != nil {
		start := *target.StartMS
		copied.StartMS = &start
	}
	if target.EndMS != nil {
		end := *target.EndMS
		copied.EndMS = &end
	}
	return copied
}

func removeAnnotationItems(doc *Annotations, match func(AnnotationItem) bool) int {
	kept := make([]AnnotationItem, 0, len(doc.Items))
	for _, item := range doc.Items {
		if !match(item) {
			kept = append(kept, item)
		}
	}
	removed := len(doc.Items) - len(kept)
	doc.Items = kept
	return removed
}

func dropUnusedAnnotationTags(doc *Annotations) {
	used := make(map[string]bool, len(doc.Items))
	for _, item := range doc.Items {
		used[item.TagID] = true
	}
	kept := make([]AnnotationTag, 0, len(doc.Tags))
	for _, tag := range doc.Tags {
		if used[tag.ID] {
			kept = append(kept, tag)
		}
	}
	doc.Tags = kept
}

// Clone copies a document for editing; nil answers an empty one.
func Clone(doc *Annotations) *Annotations {
	if doc == nil {
		return &Annotations{Tags: []AnnotationTag{}, Items: []AnnotationItem{}}
	}
	clone := *doc
	clone.Tags = append(make([]AnnotationTag, 0, len(doc.Tags)), doc.Tags...)
	clone.Items = make([]AnnotationItem, 0, len(doc.Items))
	for _, item := range doc.Items {
		item.Target = copyAnnotationTarget(item.Target)
		clone.Items = append(clone.Items, item)
	}
	return &clone
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return unique
}

const ExitInvalid = 4

type Failure struct {
	Code int
	Err  error
}

func (f *Failure) Error() string { return f.Err.Error() }
func (f *Failure) Unwrap() error { return f.Err }
func fail(code int, format string, args ...any) error {
	return &Failure{Code: code, Err: fmt.Errorf(format, args...)}
}
