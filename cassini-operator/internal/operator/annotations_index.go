package operator

import "context"

// annotationIndex exposes archive imports and vocabulary queries. The concrete
// durable store also owns desired documents and pending archive synchronization.
// Imports must preserve unconfirmed edits. Nil means writes are unavailable.
type annotationIndex interface {
	// Record replaces what is known about opusName with what `cassini annotate`
	// reported for its file.
	Record(ctx context.Context, opusName string, result annotateResult) error
	MarkUnavailable(ctx context.Context, opusName, reason string) error
	// ResolveLabel finds the tag id already used for label among visible, the
	// caller's readable meetings only.
	ResolveLabel(ctx context.Context, label string, visible []string) (tagID string, ok bool, err error)
	// TagVisible reports whether one of visible carries tagID.
	TagVisible(ctx context.Context, tagID string, visible []string) (bool, error)
	// Namespace is the tag namespace most indexed recordings carry, or "" when
	// none carries one — the CLI then keeps the file's own, or mints one on a
	// first write. It refuses with errAnnotationIndexBuilding while the first
	// rebuild of a never-built index runs.
	Namespace(ctx context.Context) (string, error)
}
