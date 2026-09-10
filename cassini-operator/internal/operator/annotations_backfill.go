package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Rebuilding the tag index from the archive (D-737).
//
// Only the DELIVERED copy in Nextcloud is read: marks are written there, and
// current/<job>.opus is the sealed artifact, which never carries one.
//
// Every delivery and conditional PUT stamps OC-Checksum with the sha256 of the
// bytes written, so a meeting whose recorded container digest equals it is
// skipped for one PROPFIND — which also makes the rebuild resumable.
//
// As in search's backfill, a failed PROPFIND or download says nothing about the
// meeting, so what was indexed before is kept; only bytes that were fetched and
// could not be read are a verdict, recorded unavailable.

// annotationArchiveReader fetches one delivered recording and reports what
// `cassini annotate show` read out of it, plus the digest of the bytes fetched.
type annotationArchiveReader func(ctx context.Context, opusName string) (annotateResult, string, error)

// annotationsUnreadableError is a recording that was fetched but could not be
// read — a verdict about those bytes, unlike a download that failed.
type annotationsUnreadableError struct{ err error }

func (e *annotationsUnreadableError) Error() string { return "read annotations: " + e.err.Error() }
func (e *annotationsUnreadableError) Unwrap() error { return e.err }

// archiveAnnotationReader downloads a recording as the recordings owner — an
// administrative rebuild whose every answer is filtered by the caller's own
// visibility — and asks the CLI for its annotations.
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
			// Only the CLI's own refusal is about the bytes.
			if annotateExitCode(err) != 0 && ctx.Err() == nil {
				return annotateResult{}, digest, &annotationsUnreadableError{err: err}
			}
			return annotateResult{}, digest, err
		}
		return result, digest, nil
	}
}

// annotationBackfillReport is what a rebuild did.
type annotationBackfillReport struct {
	Indexed   int
	Unchanged int // already recorded from these bytes; nothing downloaded
	// Unavailable: read, and its marks could not be — recorded as such.
	Unavailable int
	// Failed: could not be asked; retryable, and existing rows are kept.
	Failed    int
	Forgotten int // dropped because the archive no longer names them
}

type annotationBackfillOutcome int

const (
	annotationBackfillFailed annotationBackfillOutcome = iota
	annotationBackfillIndexed
	annotationBackfillUnchanged
	annotationBackfillUnavailable
)

// backfillAnnotationIndex records every delivered recording's marks, then marks
// the index built. One meeting's failure never stops the run.
func backfillAnnotationIndex(
	ctx context.Context, store *annotationStore, logger *log.Logger,
	targets []searchBackfillTarget, delivered searchDeliveredStateReader, archive annotationArchiveReader,
) (annotationBackfillReport, error) {
	var report annotationBackfillReport
	if store == nil {
		return report, errors.New("annotations index is not open")
	}
	if delivered == nil || archive == nil {
		return report, errors.New("the rebuild needs the archive")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	recorded, err := store.recordedContainers(ctx)
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
	if err := store.markBuilt(ctx); err != nil {
		// Only the marker is missing: the next start rebuilds again, which is
		// wasted work, never a wrong answer.
		logger.Printf("annotations backfill: %v", err)
	}
	return report, nil
}

func backfillOneAnnotation(
	ctx context.Context, store *annotationStore, opusName string, recorded map[string]string,
	delivered searchDeliveredStateReader, archive annotationArchiveReader,
) (annotationBackfillOutcome, string) {
	checksum, exists, err := delivered(ctx, opusName)
	if err != nil {
		return annotationBackfillFailed, fmt.Sprintf("read delivered state: %v", err)
	}
	if !exists {
		// Drift this run cannot judge; pruned once the catalog stops naming it.
		return annotationBackfillFailed, "the catalog names it but the archive does not hold it"
	}
	existing, known := recorded[opusName]
	if sameContainer(existing, checksum) {
		return annotationBackfillUnchanged, ""
	}

	result, digest, err := archive(ctx, opusName)
	if err != nil {
		var unreadable *annotationsUnreadableError
		if errors.As(err, &unreadable) {
			if err := store.MarkUnavailable(ctx, opusName, ""); err != nil {
				return annotationBackfillFailed, fmt.Sprintf("record it unreadable: %v", err)
			}
			return annotationBackfillUnavailable, err.Error()
		}
		if !known {
			// Nothing to protect; at least make the meeting known, outside
			// coverage. Still a failure: re-running is the fix.
			_ = store.MarkUnavailable(ctx, opusName, "")
		}
		return annotationBackfillFailed, fmt.Sprintf("archive recording unreadable: %v", err)
	}
	// The digest of the bytes actually read is what OC-Checksum is compared
	// against next time.
	if digest = strings.ToLower(strings.TrimSpace(digest)); digest != "" {
		result.ContainerSHA256 = digest
	}
	if sameContainer(existing, result.ContainerSHA256) {
		// Delivered before uploads carried a checksum: downloaded to learn its
		// digest, and unchanged.
		return annotationBackfillUnchanged, ""
	}
	state, err := store.record(ctx, opusName, result, false)
	if err != nil {
		return annotationBackfillFailed, fmt.Sprintf("write rows: %v", err)
	}
	if state == annotationsStateUnavailable {
		return annotationBackfillUnavailable, "annotations format unsupported"
	}
	return annotationBackfillIndexed, ""
}

// sameContainer reports whether a meeting was last recorded from these bytes;
// an unknown digest on either side is never a match.
func sameContainer(existing, digest string) bool {
	digest = strings.TrimSpace(digest)
	return digest != "" && existing != "" && strings.EqualFold(existing, digest)
}

// forgetVanishedAnnotations drops meetings the archive no longer names, so a
// deleted recording's labels stop feeding ResolveLabel. Never on an empty
// target list, which is indistinguishable from an outage.
func forgetVanishedAnnotations(
	ctx context.Context, store *annotationStore, logger *log.Logger,
	targets []searchBackfillTarget, recorded map[string]string,
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
		if err := store.inTx(ctx, func(tx *sql.Tx) error { return deleteAnnotationRows(ctx, tx, name) }); err != nil {
			return forgotten, err
		}
		logger.Printf("annotations backfill: %s is no longer in the archive; dropped from the index", name)
		forgotten++
	}
	return forgotten, nil
}
