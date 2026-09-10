package cassini

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

	"gocassini/internal/portable"
)

// The ops `cassini annotate apply` takes, and the pure function that applies
// them to a document. Nothing in this file touches a file: the batch is worked
// out in memory first, so a batch that turns out to be invalid halfway through
// has written nothing, and the whole of it is decided before the one rewrite.

const (
	annotateOpMark          = "mark"
	annotateOpUnmark        = "unmark"
	annotateOpUnmarkTag     = "unmark-tag"
	annotateOpUndoOperation = "undo-operation"
	annotateOpRelabel       = "relabel"
)

// annotateOpFields is the set of members each op may carry besides "op". A
// member that belongs to another op is refused rather than ignored: an unmark
// sent with a tagId is a caller that meant unmark-tag, and quietly doing
// something else is how a batch removes the wrong marks.
var annotateOpFields = map[string][]string{
	annotateOpMark:          {"tag", "target"},
	annotateOpUnmark:        {"itemId"},
	annotateOpUnmarkTag:     {"tagId", "target"},
	annotateOpUndoOperation: {"operationId"},
	annotateOpRelabel:       {"tagId", "label"},
}

// annotateOp is one entry of an ops document. It is the union of every op's
// members; annotateOpFields says which ones each op may use.
type annotateOp struct {
	Op          string                     `json:"op"`
	Tag         *annotateOpTag             `json:"tag"`
	Target      *portable.AnnotationTarget `json:"target"`
	ItemID      string                     `json:"itemId"`
	TagID       string                     `json:"tagId"`
	OperationID string                     `json:"operationId"`
	Label       *string                    `json:"label"`
}

// annotateOpTag names the tag a mark applies. Label is a pointer because
// "absent" and "empty" mean different things: absent means "use the tag with
// this id", empty is an invalid label.
type annotateOpTag struct {
	ID    string  `json:"id"`
	Label *string `json:"label"`
}

// annotateStamp is what every item a batch adds is stamped with.
type annotateStamp struct {
	ActorKind   string
	ActorID     string
	OperationID string
	CreatedAt   string
}

// annotateOpsOutcome is a batch, worked out.
type annotateOpsOutcome struct {
	// Doc holds the tags and items after the batch, with unused tags dropped
	// and in canonical order. Its format, revision, binding and namespace are
	// the current document's; setting them for the write is the caller's job.
	Doc *portable.Annotations
	// Changed is false when the batch leaves the tags and items exactly as they
	// were — which is not a commit, and is not written.
	Changed  bool
	Added    []string
	Removed  []string
	NotFound []string
}

// parseAnnotateOps decodes an ops document strictly. Unknown members are
// refused at every level: a misspelt "tagid" silently read as absent would
// turn a targeted removal into a removal of everything.
func parseAnnotateOps(raw []byte) ([]annotateOp, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope struct {
		Ops []json.RawMessage `json:"ops"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		return nil, annotateFail(annotateExitInvalid, `the ops document is not {"ops":[...]}: %v`, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, annotateFail(annotateExitInvalid, "the ops document has content after its closing brace")
	}
	if envelope.Ops == nil {
		return nil, annotateFail(annotateExitInvalid, `the ops document has no "ops" array`)
	}

	ops := make([]annotateOp, 0, len(envelope.Ops))
	for i, rawOp := range envelope.Ops {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(rawOp, &members); err != nil || members == nil {
			return nil, annotateFail(annotateExitInvalid, "ops[%d]: not a JSON object", i)
		}
		opDecoder := json.NewDecoder(bytes.NewReader(rawOp))
		opDecoder.DisallowUnknownFields()
		var op annotateOp
		if err := opDecoder.Decode(&op); err != nil {
			return nil, annotateFail(annotateExitInvalid, "ops[%d]: %v", i, err)
		}
		allowed, known := annotateOpFields[op.Op]
		if !known {
			return nil, annotateFail(annotateExitInvalid,
				"ops[%d]: unknown op %q (want mark, unmark, unmark-tag, undo-operation or relabel)", i, op.Op)
		}
		// Checked on the exact spelling: encoding/json matches member names
		// case-insensitively, so "TagId" decoded fine above.
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if name != "op" && !slices.Contains(allowed, name) {
				return nil, annotateFail(annotateExitInvalid, "ops[%d] (%s): %q is not a member of this op", i, op.Op, name)
			}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// applyAnnotationOps works out a batch against the current document (nil when
// the file carries none), in order, and without touching current. durationMS
// bounds every time range an op names.
//
// An op the caller got wrong fails the whole batch with exit 4 and a message
// naming the op; nothing is partially applied, because nothing is written until
// the whole batch has been worked out.
func applyAnnotationOps(current *portable.Annotations, ops []annotateOp, durationMS int64, stamp annotateStamp) (annotateOpsOutcome, error) {
	before := cloneAnnotations(current)
	before.Canonicalize()
	work := cloneAnnotations(current)
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
		default:
			err = annotateFail(annotateExitInvalid, "unknown op")
		}
		if err != nil {
			var failure *annotateFailure
			if errors.As(err, &failure) {
				return annotateOpsOutcome{}, &annotateFailure{code: failure.code, err: fmt.Errorf("ops[%d] (%s): %w", i, op.Op, failure.err)}
			}
			return annotateOpsOutcome{}, err
		}
	}

	// A tag with no marks left is not a tag in this file any more. Keeping the
	// definition would list it in the vocabulary as present on a meeting it is
	// no longer on.
	dropUnusedAnnotationTags(work)
	work.Canonicalize()

	outcome := annotateOpsOutcome{
		Doc:      work,
		NotFound: uniqueStrings(notFound),
		Added:    []string{},
		Removed:  []string{},
	}
	// Both sides are in canonical order, which is a total order (ids are
	// unique), so equal content means equal slices.
	outcome.Changed = !reflect.DeepEqual(before.Tags, work.Tags) || !reflect.DeepEqual(before.Items, work.Items)

	// Net, from the two id sets rather than tallied per op, so an item added and
	// removed within one batch is reported as neither.
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

// applyMarkOp ensures the tag, then adds the item unless an identical one is
// already there.
//
// The tag is found by id when the id is defined in this file — a differing
// label is then ignored, since a mark is not a rename — else by label within
// this file, else defined. Matching by label within the file is what keeps one
// file from carrying two tags that read the same; mapping a label to one id
// across the whole archive is the operator's job, before it calls this.
//
// The identical-item check is the idempotence that makes a retry safe: the
// same request sent twice after a lost response lands once.
func applyMarkOp(doc *portable.Annotations, op annotateOp, durationMS int64, stamp annotateStamp) error {
	if op.Tag == nil {
		return annotateFail(annotateExitInvalid, `needs a tag: {"id"?, "label"?}`)
	}
	if op.Target == nil {
		return annotateFail(annotateExitInvalid, "needs a target")
	}
	label, hasLabel, err := annotateOpLabel(op.Tag.Label, "tag.label")
	if err != nil {
		return err
	}
	if op.Tag.ID == "" && !hasLabel {
		return annotateFail(annotateExitInvalid, "the tag needs an id or a label")
	}
	target := copyAnnotationTarget(*op.Target)
	if err := checkAnnotateTarget(target, durationMS); err != nil {
		return err
	}

	tagID := findAnnotationTag(doc, op.Tag.ID, label, hasLabel)
	if tagID == "" {
		if !hasLabel {
			return annotateFail(annotateExitInvalid, "tag %q is not defined in this file, and the op carries no label to define it with", op.Tag.ID)
		}
		tagID = op.Tag.ID
		if tagID == "" {
			// Random, never time-ordered: see portable.NewAnnotationTagID.
			if tagID, err = portable.NewAnnotationTagID(); err != nil {
				return err
			}
		}
		doc.Tags = append(doc.Tags, portable.AnnotationTag{ID: tagID, Label: label})
	}

	for _, item := range doc.Items {
		if item.TagID == tagID && sameAnnotationTarget(item.Target, target) {
			return nil
		}
	}
	itemID, err := portable.NewAnnotationItemID()
	if err != nil {
		return err
	}
	doc.Items = append(doc.Items, portable.AnnotationItem{
		ID:           itemID,
		TagID:        tagID,
		Target:       target,
		CreatedAtUTC: stamp.CreatedAt,
		Actor:        portable.AnnotationActor{Kind: stamp.ActorKind, ID: stamp.ActorID},
		OperationID:  stamp.OperationID,
	})
	return nil
}

func applyUnmarkOp(doc *portable.Annotations, op annotateOp, notFound *[]string) error {
	if op.ItemID == "" {
		return annotateFail(annotateExitInvalid, "needs an itemId")
	}
	if removeAnnotationItems(doc, func(item portable.AnnotationItem) bool { return item.ID == op.ItemID }) == 0 {
		*notFound = append(*notFound, op.ItemID)
	}
	return nil
}

func applyUnmarkTagOp(doc *portable.Annotations, op annotateOp, durationMS int64, notFound *[]string) error {
	if op.TagID == "" {
		return annotateFail(annotateExitInvalid, "needs a tagId")
	}
	var target *portable.AnnotationTarget
	if op.Target != nil {
		checked := copyAnnotationTarget(*op.Target)
		if err := checkAnnotateTarget(checked, durationMS); err != nil {
			return err
		}
		target = &checked
	}
	removed := removeAnnotationItems(doc, func(item portable.AnnotationItem) bool {
		return item.TagID == op.TagID && (target == nil || sameAnnotationTarget(item.Target, *target))
	})
	if removed == 0 {
		*notFound = append(*notFound, op.TagID)
	}
	return nil
}

func applyUndoOperationOp(doc *portable.Annotations, op annotateOp, notFound *[]string) error {
	if op.OperationID == "" {
		return annotateFail(annotateExitInvalid, "needs an operationId")
	}
	if removeAnnotationItems(doc, func(item portable.AnnotationItem) bool { return item.OperationID == op.OperationID }) == 0 {
		*notFound = append(*notFound, op.OperationID)
	}
	return nil
}

// applyRelabelOp renames a tag in this file only; renaming across the archive
// rewrites every file carrying the tag and is D-746's. A label another tag in
// this file already reads as is refused: the file would then carry two tags
// with one name, and a mark by label could no longer say which it meant.
func applyRelabelOp(doc *portable.Annotations, op annotateOp, notFound *[]string) error {
	if op.TagID == "" {
		return annotateFail(annotateExitInvalid, "needs a tagId")
	}
	if op.Label == nil {
		return annotateFail(annotateExitInvalid, "needs a label")
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
			return annotateFail(annotateExitInvalid, "label %q is already tag %q's in this file", label, tag.ID)
		}
	}
	doc.Tags[index].Label = label
	return nil
}

// annotateOpLabel trims a label from an op and checks it as the format stores
// it. absent answers ("", false, nil).
func annotateOpLabel(raw *string, member string) (string, bool, error) {
	if raw == nil {
		return "", false, nil
	}
	label := strings.TrimSpace(*raw)
	if err := portable.ValidateAnnotationLabel(label); err != nil {
		return "", false, annotateFail(annotateExitInvalid, "%s: %v", member, err)
	}
	return label, true, nil
}

// checkAnnotateTarget checks a target an op names, so a bad one is refused with
// a message pointing at the op rather than at an index in the rewritten
// document. It restates portable's target rules; ValidateAnnotations runs on
// the whole document before the write regardless, so this can only ever be the
// earlier of two refusals.
func checkAnnotateTarget(target portable.AnnotationTarget, durationMS int64) error {
	switch target.Kind {
	case portable.AnnotationTargetMeeting:
		if target.StartMS != nil || target.EndMS != nil {
			return annotateFail(annotateExitInvalid, "target: a meeting target carries no startMs or endMs")
		}
		return nil
	case portable.AnnotationTargetTimeRange:
		if target.StartMS == nil || target.EndMS == nil {
			return annotateFail(annotateExitInvalid, "target: a time-range target needs both startMs and endMs")
		}
		start, end := *target.StartMS, *target.EndMS
		switch {
		case start < 0:
			return annotateFail(annotateExitInvalid, "target: startMs %d is negative", start)
		case end <= start:
			return annotateFail(annotateExitInvalid, "target: [%d, %d) is empty or reversed", start, end)
		case durationMS <= 0:
			return annotateFail(annotateExitInvalid, "target: the recording's duration is unknown, so a time range cannot be checked")
		case end > durationMS:
			return annotateFail(annotateExitInvalid, "target: endMs %d is past the end of the audio (%d)", end, durationMS)
		}
		return nil
	default:
		return annotateFail(annotateExitInvalid, "target: unknown kind %q (want %s or %s)", target.Kind, portable.AnnotationTargetMeeting, portable.AnnotationTargetTimeRange)
	}
}

// findAnnotationTag answers the id of the tag a mark applies to, or "" when it
// must be defined: by id when that id is defined here, else by label. Labels
// are compared as the design says — trimmed (they already are) and
// case-insensitively, with no Unicode normalisation.
func findAnnotationTag(doc *portable.Annotations, id, label string, hasLabel bool) string {
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

func sameAnnotationTarget(a, b portable.AnnotationTarget) bool {
	return a.Kind == b.Kind && sameInt64Pointer(a.StartMS, b.StartMS) && sameInt64Pointer(a.EndMS, b.EndMS)
}

func sameInt64Pointer(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func copyAnnotationTarget(target portable.AnnotationTarget) portable.AnnotationTarget {
	copied := portable.AnnotationTarget{Kind: target.Kind}
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

// removeAnnotationItems removes every item match accepts and answers how many.
func removeAnnotationItems(doc *portable.Annotations, match func(portable.AnnotationItem) bool) int {
	kept := make([]portable.AnnotationItem, 0, len(doc.Items))
	for _, item := range doc.Items {
		if !match(item) {
			kept = append(kept, item)
		}
	}
	removed := len(doc.Items) - len(kept)
	doc.Items = kept
	return removed
}

func dropUnusedAnnotationTags(doc *portable.Annotations) {
	used := make(map[string]bool, len(doc.Items))
	for _, item := range doc.Items {
		used[item.TagID] = true
	}
	kept := make([]portable.AnnotationTag, 0, len(doc.Tags))
	for _, tag := range doc.Tags {
		if used[tag.ID] {
			kept = append(kept, tag)
		}
	}
	doc.Tags = kept
}

// cloneAnnotations copies a document so it can be edited without touching the
// original — nil answers an empty one. Slices are always non-nil, so a document
// with nothing in it writes "tags": [] rather than "tags": null.
func cloneAnnotations(doc *portable.Annotations) *portable.Annotations {
	if doc == nil {
		return &portable.Annotations{Tags: []portable.AnnotationTag{}, Items: []portable.AnnotationItem{}}
	}
	clone := *doc
	clone.Tags = append(make([]portable.AnnotationTag, 0, len(doc.Tags)), doc.Tags...)
	clone.Items = make([]portable.AnnotationItem, 0, len(doc.Items))
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
