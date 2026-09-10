package operator

import "context"

// annotationIndex is the projection of every recording's marks,
// annotations.sqlite3 (D-737). The file is always the record and this is only a
// rebuildable copy: a write commits the file first and records here second, and
// a failure here costs coverage, never a mark.
//
// The interface holds only what the write endpoint and the publish path call.
// The store that implements it (annotations_store.go) also serves its own read
// routes — the vocabulary, and tag narrowing for search and the meeting list —
// which need nothing from this seam. Nil wherever the store could not be opened;
// callers skip indexing and log, as search's ingest does.
type annotationIndex interface {
	// Record replaces everything known about opusName with what its file now
	// carries. result is what `cassini annotate` reported for that file.
	Record(ctx context.Context, opusName string, result annotateResult) error
	// MarkUnavailable records that opusName's marks could not be read, so the
	// vocabulary reports partial coverage instead of a false complete one.
	MarkUnavailable(ctx context.Context, opusName, reason string) error
	// ResolveLabel maps a label (trimmed, case-insensitive) to the tag id this
	// installation already uses for it anywhere in the archive. ok is false when
	// none does, and the caller mints one.
	ResolveLabel(ctx context.Context, label string) (tagID string, ok bool, err error)
	// Namespace is this installation's tag namespace: adopted from the archive's
	// own files on a fresh volume, minted only when the archive carries none.
	Namespace(ctx context.Context) (string, error)
}
