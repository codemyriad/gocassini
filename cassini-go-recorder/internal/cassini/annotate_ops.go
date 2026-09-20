package cassini

import (
	annotations "cassini-annotations"
	"errors"
)

type annotateOp = annotations.Op
type annotateOpTag = annotations.OpTag
type annotateStamp = annotations.Stamp
type annotateOpsOutcome = annotations.Outcome
type annotateBatch = annotations.Batch

func parseAnnotateOps(raw []byte) ([]annotateOp, error) {
	ops, err := annotations.ParseOps(raw)
	return ops, annotationOpsFailure(err)
}
func parseAnnotateBatch(raw []byte) (annotateBatch, error) {
	batch, err := annotations.ParseBatch(raw)
	return batch, annotationOpsFailure(err)
}
func applyAnnotationOps(current *annotations.Annotations, ops []annotateOp, duration int64, stamp annotateStamp) (annotateOpsOutcome, error) {
	outcome, err := annotations.ApplyOps(current, ops, duration, stamp)
	return outcome, annotationOpsFailure(err)
}
func applyAnnotationBatch(current *annotations.Annotations, batch annotateBatch, duration int64, stamp annotateStamp) (annotateOpsOutcome, error) {
	outcome, err := annotations.ApplyBatch(current, batch, duration, stamp)
	return outcome, annotationOpsFailure(err)
}
func annotationOpsFailure(err error) error {
	var shared *annotations.Failure
	if errors.As(err, &shared) {
		return &annotateFailure{code: shared.Code, err: shared.Err}
	}
	return err
}

var cloneAnnotations = annotations.Clone

const annotateOpMark = "mark"
const annotateOpUnmark = "unmark"
const annotateOpUnmarkTag = "unmark-tag"
const annotateOpUndoOperation = "undo-operation"
const annotateOpRelabel = "relabel"
const annotateOpMergeTag = "merge-tag"
const annotateOpRestyle = "restyle"
