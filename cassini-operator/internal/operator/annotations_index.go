package operator

import "context"

// annotationIndex is the part of the marks projection, annotations.sqlite3, that
// the write endpoint and the publish path call (D-737). The file is the record;
// this is a rebuildable copy, written second, so a failure costs coverage and
// never a mark. Nil where the store could not be opened.
type annotationIndex interface {
	// Record replaces what is known about opusName with what `cassini annotate`
	// reported for its file.
	Record(ctx context.Context, opusName string, result annotateResult) error
	MarkUnavailable(ctx context.Context, opusName, reason string) error
	// ResolveLabel finds the tag id already used for label among visible, the
	// caller's readable meetings only.
	ResolveLabel(ctx context.Context, label string, visible []string) (tagID string, ok bool, err error)
	// Namespace is the tag namespace most indexed recordings carry, or "" when
	// none carries one — the CLI then keeps the file's own, or mints one on a
	// first write. It refuses with errAnnotationIndexBuilding while the first
	// rebuild of a never-built index runs.
	Namespace(ctx context.Context) (string, error)
}
