package cassini

import annotations "cassini-annotations"

type annotateOp = annotations.Op
type annotateOpTag = annotations.OpTag
type annotateStamp = annotations.Stamp
type annotateOpsOutcome = annotations.Outcome

var parseAnnotateOps = annotations.ParseOps
var applyAnnotationOps = annotations.ApplyOps
var cloneAnnotations = annotations.Clone

const annotateOpMark = "mark"
const annotateOpUnmark = "unmark"
const annotateOpUnmarkTag = "unmark-tag"
const annotateOpUndoOperation = "undo-operation"
const annotateOpRelabel = "relabel"
const annotateOpMergeTag = "merge-tag"
