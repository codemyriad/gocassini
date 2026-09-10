package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Rebuilding the marks projection from the archive (D-737).
//
// WHY IT READS ONLY THE DELIVERED COPY
//
// Marks are written into the DELIVERED .opus — by the service account after a
// POST, and carried forward onto it by a rerun's publish. current/<job>.opus is
// the sealed artifact the pipeline produced and never carries one. Search's
// backfill prefers current/ because it holds richer segments than the archive;
// here current/ holds strictly less, and reading it would record every meeting
// as unmarked. So this reads Nextcloud and nothing else.
//
// WHY THE SKIP COSTS ONE PROPFIND
//
// Every delivery and every conditional PUT stamps OC-Checksum with the sha256
// of the bytes written (webdav_upload.go), so the leaf's checksum is its
// container digest. A meeting whose recorded container_sha256 equals it was
// indexed from these very bytes and is not downloaded again. That is also what
// makes the rebuild resumable: an interrupted run's finished meetings are
// skipped by the next one.
//
// A FAILED READ IS NOT A VERDICT
//
// Search's rule, for search's reason (search_backfill.go): a PROPFIND or
// download that fails says nothing about the meeting, so a meeting already in
// the projection keeps its rows and the operator re-runs. Only a file that was
// fetched and could not be read is a verdict, recorded unavailable.

const (
	annotationsBackfillReasonArchiveUnread = "archive-recording-unreadable"
)

// annotationArchiveReader fetches one delivered recording and reports what
// `cassini annotate show` read out of it, plus the digest of the bytes fetched.
// A function so the rebuild is testable without a Nextcloud.
type annotationArchiveReader func(ctx context.Context, opusName string) (annotateResult, string, error)

// annotationsUnreadableError is a recording that was fetched but could not be
// read — a verdict about those bytes, unlike a download that failed.
type annotationsUnreadableError struct{ err error }

func (e *annotationsUnreadableError) Error() string { return "read annotations: " + e.err.Error() }
func (e *annotationsUnreadableError) Unwrap() error { return e.err }

// archiveAnnotationReader downloads a recording as the recordings owner and asks
// the CLI for its annotations.
//
// Owner rather than caller for the reason archiveOpusReader gives: this is an
// administrative rebuild of a projection whose every answer is filtered by the
// caller's own visibility, and it grants nobody anything.
func (c ExAppConfig) archiveAnnotationReader(cassiniBin, workDir string) annotationArchiveReader {
	client := &http.Client{Timeout: archiveOpusReadTimeout}
	return func(ctx context.Context, opusName string) (annotateResult, string, error) {
		tmp, err := os.MkdirTemp(workDir, "annotations-backfill-")
		if err != nil {
			return annotateResult{}, "", fmt.Errorf("temp dir: %w", err)
		}
		defer os.RemoveAll(tmp)

		local := filepath.Join(tmp, opusName)
		digest, err := c.downloadArchiveOpus(ctx, client, opusName, local)
		if err != nil {
			return annotateResult{}, "", err
		}
		result, err := runAnnotateShow(ctx, cassiniBin, local)
		if err != nil {
			// Only the CLI's own refusal is about the bytes. A binary that
			// would not start, or a run cut short, says nothing about them.
			if annotateExitCode(err) != 0 && ctx.Err() == nil {
				return annotateResult{}, digest, &annotationsUnreadableError{err: err}
			}
			return annotateResult{}, digest, err
		}
		return result, digest, nil
	}
}

// annotationBackfillReport is what a rebuild did, in the terms an operator
// acts on.
type annotationBackfillReport struct {
	// Indexed: the meeting's marks were read and recorded.
	Indexed int
	// Unchanged: already recorded from these very bytes; nothing downloaded.
	Unchanged int
	// Unavailable: the file was read and its marks could not be — an unknown
	// format, or a document the CLI refused. Recorded with a reason.
	Unavailable int
	// Failed: could not be asked — the archive did not answer, or the
	// projection rejected the write. Retryable; existing rows are kept.
	Failed int
	// Forgotten: rows dropped for meetings the archive no longer names.
	Forgotten int
	// Namespace is what the rebuild did about the installation's namespace.
	Namespace namespaceAdoption
}

type annotationBackfillOutcome int

const (
	annotationBackfillFailed annotationBackfillOutcome = iota
	annotationBackfillIndexed
	annotationBackfillUnchanged
	annotationBackfillUnavailable
)

// backfillAnnotationIndex records every delivered recording's marks, then
// adopts the archive's namespace if none is stored yet.
//
// Never stopped by one meeting: an archive with a single unreadable recording
// should still end up with every other meeting's marks indexed.
func backfillAnnotationIndex(
	ctx context.Context, store *annotationStore, logger *log.Logger,
	targets []searchBackfillTarget, delivered searchDeliveredStateReader, archive annotationArchiveReader,
) (annotationBackfillReport, error) {
	var report annotationBackfillReport
	if store == nil {
		return report, errors.New("annotations index is not open")
	}
	if delivered == nil || archive == nil {
		// Without the archive there is nothing to rebuild FROM, and current/
		// is not a substitute (see above).
		return report, errors.New("the rebuild needs the archive")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	recorded, err := store.recordedState(ctx)
	if err != nil {
		return report, err
	}

	for _, target := range targets {
		name := strings.TrimSpace(target.OpusName)
		if name == "" {
			logger.Printf("annotations backfill: job %s has no join key; skipped", target.JobID)
			report.Failed++
			continue
		}
		outcome, reason := backfillOneAnnotation(ctx, store, name, recorded, delivered, archive)
		switch outcome {
		case annotationBackfillIndexed:
			report.Indexed++
		case annotationBackfillUnchanged:
			report.Unchanged++
		case annotationBackfillUnavailable:
			report.Unavailable++
			logger.Printf("annotations backfill: %s marks unreadable (%s)", name, reason)
		default:
			report.Failed++
			logger.Printf("annotations backfill: %s failed (%s)", name, reason)
		}
	}

	forgotten, err := forgetVanishedAnnotations(ctx, store, logger, targets, recorded)
	if err != nil {
		// Housekeeping: failing it must not discard a run that indexed.
		logger.Printf("annotations backfill: could not prune vanished meetings (%v)", err)
	}
	report.Forgotten = forgotten

	// After every meeting is recorded, so "most common" is counted over the
	// whole archive rather than whichever file happened to come first.
	adoption, err := store.adoptNamespace(ctx)
	if err != nil {
		// Not fatal: Namespace() adopts the same way on first use.
		logger.Printf("annotations backfill: could not adopt the archive's tag namespace (%v)", err)
	}
	report.Namespace = adoption
	if err := store.markBuilt(ctx); err != nil {
		// The rows are in; only the marker is missing, so the next start rebuilds
		// again — wasted work, never a wrong answer.
		logger.Printf("annotations backfill: %v", err)
	}
	return report, nil
}

func backfillOneAnnotation(
	ctx context.Context, store *annotationStore, opusName string, recorded map[string]recordedAnnotations,
	delivered searchDeliveredStateReader, archive annotationArchiveReader,
) (annotationBackfillOutcome, string) {
	checksum, exists, err := delivered(ctx, opusName)
	if err != nil {
		return annotationBackfillFailed, fmt.Sprintf("read delivered state: %v", err)
	}
	if !exists {
		// The catalog names it but the leaf is gone: drift this run cannot
		// judge. Convergence prunes it once the catalog stops naming it.
		return annotationBackfillFailed, "the catalog names it but the archive does not hold it"
	}
	existing, known := recorded[opusName]
	if sameContainer(existing, known, checksum) {
		return annotationBackfillUnchanged, ""
	}

	result, digest, err := archive(ctx, opusName)
	if err != nil {
		var unreadable *annotationsUnreadableError
		if errors.As(err, &unreadable) {
			if err := store.MarkUnavailable(ctx, opusName, annotationsReasonUnreadable); err != nil {
				return annotationBackfillFailed, fmt.Sprintf("record %s: %v", annotationsReasonUnreadable, err)
			}
			return annotationBackfillUnavailable, annotationsReasonUnreadable
		}
		if !known {
			// Nothing to protect, and the meeting should at least be known to
			// the projection with a reason — it already counts outside coverage
			// either way. Still a failure: re-running is the fix.
			_ = store.MarkUnavailable(ctx, opusName, annotationsBackfillReasonArchiveUnread)
		}
		return annotationBackfillFailed, fmt.Sprintf("%s: %v", annotationsBackfillReasonArchiveUnread, err)
	}
	// Record the digest of the bytes actually read. It is what OC-Checksum
	// will be compared against next time.
	if digest = strings.ToLower(strings.TrimSpace(digest)); digest != "" {
		result.ContainerSHA256 = digest
	}
	if sameContainer(existing, known, result.ContainerSHA256) {
		// A recording with no OC-Checksum (delivered before uploads carried
		// one) had to be downloaded to learn its digest, and it has not changed.
		return annotationBackfillUnchanged, ""
	}
	state, err := store.record(ctx, opusName, result, false) // authoritative: it read the file itself
	if err != nil {
		return annotationBackfillFailed, fmt.Sprintf("write rows: %v", err)
	}
	if state == annotationsStateUnavailable {
		return annotationBackfillUnavailable, annotationsReasonUnsupported
	}
	return annotationBackfillIndexed, ""
}

// sameContainer reports whether the meeting was last recorded from these bytes.
// An unknown digest on either side is never a match.
func sameContainer(existing recordedAnnotations, known bool, digest string) bool {
	digest = strings.ToLower(strings.TrimSpace(digest))
	return known && digest != "" && existing.container != "" && strings.EqualFold(existing.container, digest)
}

// forgetVanishedAnnotations drops rows for meetings the archive no longer
// names, so a deleted recording's labels stop feeding ResolveLabel. Guarded on
// a non-empty target list for search's reason: an archive read that came back
// empty is indistinguishable from an empty archive.
func forgetVanishedAnnotations(
	ctx context.Context, store *annotationStore, logger *log.Logger,
	targets []searchBackfillTarget, recorded map[string]recordedAnnotations,
) (int, error) {
	if len(targets) == 0 {
		return 0, nil
	}
	present := make(map[string]bool, len(targets))
	for _, target := range targets {
		present[strings.TrimSpace(target.OpusName)] = true
	}
	forgotten := 0
	for name := range recorded {
		if present[name] {
			continue
		}
		if err := store.ForgetMeeting(ctx, name); err != nil {
			return forgotten, err
		}
		logger.Printf("annotations backfill: %s is no longer in the archive; dropped from the index", name)
		forgotten++
	}
	return forgotten, nil
}
