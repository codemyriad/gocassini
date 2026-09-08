package operator

import (
	"context"
	"fmt"
	"strings"
)

// Backfill: making meetings published before the index existed searchable
// (D-623).
//
// WHY IT READS current/ AND NOT THE ARCHIVE
//
// The obvious source is the published `.opus` itself, and it is the wrong one
// for two reasons. It carries the RAW word transcript and nothing else — D-695
// decoded all 128 published meetings and found every transcript role to be
// raw-asr — so indexing from it produces the coarse word-derived rows rather
// than the producer's segments. And reading one means downloading it and
// shelling to ffprobe, because there is no pure-Go reader for a portable
// `.opus` in this tree; across a whole archive that is gigabytes of transfer to
// recover a worse index than the one already available locally.
//
// current/<job>.meeting holds the promoted bundle, is NEVER pruned
// (retention.go), and carries segments. So backfill reads that.
//
// THE PRICE OF READING current/, AND HOW IT IS PAID
//
// current/ tracks the last attempt that BUILT, not the last that was
// DELIVERED — promoteMeetingBundle runs in the build worker. A rerun whose
// build succeeded and whose seal or publish then failed leaves a transcript
// there that does not match the `.opus` Nextcloud is serving. Indexing it would
// have search cite words that are not in the recording anyone can play.
//
// So every meeting is checked before it is indexed: the digest of
// current/<job>.opus must equal the digest the job recorded for the artifact it
// delivered. A mismatch is recorded as unavailable with a reason, never
// indexed and never silently skipped — a meeting missing from the covered count
// is a partial answer, while a meeting indexed from the wrong attempt is a
// confident wrong one.
//
// This is also why backfill does not fall back to the `.opus` on a mismatch:
// the correct fix is to re-run the job, not to quietly index a coarser and
// differently-worded transcript under the same name.

const (
	searchBackfillReasonNoBundle    = "bundle-missing"
	searchBackfillReasonNoDigest    = "delivered-digest-unknown"
	searchBackfillReasonStaleBundle = "bundle-newer-than-delivered"
	searchBackfillReasonUnreadable  = "transcript-unreadable"
	searchBackfillReasonNoSegments  = "transcript-has-no-segments"
)

// searchBackfillTarget is one published meeting to consider.
//
// OpusName is the join key, and it comes from the archive's own catalog entry —
// the same string the per-caller visibility scan returns. It is passed in
// rather than derived here so the caller owns that read, and so this is
// testable without a Nextcloud.
type searchBackfillTarget struct {
	JobID    string
	OpusName string
}

// searchBackfillReport is what a run did, in the terms an operator acts on.
type searchBackfillReport struct {
	// Indexed: rows written.
	Indexed int
	// Unchanged: already indexed from the same delivered artifact.
	Unchanged int
	// Unavailable: recorded as not searchable, with a reason.
	Unavailable int
	// Failed: could not even be recorded — no join key, or the index rejected
	// the write. These are the ones a retry might fix.
	Failed int
}

// backfillSearchIndex indexes each target that can be indexed safely.
//
// It never stops on one meeting's failure: an archive with a single unreadable
// bundle should still end up with every other meeting searchable.
func (rt *Runtime) backfillSearchIndex(ctx context.Context, targets []searchBackfillTarget) (searchBackfillReport, error) {
	var report searchBackfillReport
	if rt.searchStore == nil {
		return report, fmt.Errorf("search index is not open")
	}
	indexed, err := rt.searchStore.indexedDigests(ctx)
	if err != nil {
		return report, err
	}

	for _, target := range targets {
		name := strings.TrimSpace(target.OpusName)
		if name == "" {
			// Nothing to key a row or a failure on.
			rt.logger.Printf("search backfill: job %s has no join key; skipped", target.JobID)
			report.Failed++
			continue
		}
		outcome, reason := rt.backfillOneMeeting(ctx, target, name, indexed)
		switch outcome {
		case searchBackfillIndexed:
			report.Indexed++
		case searchBackfillUnchanged:
			report.Unchanged++
		case searchBackfillUnavailable:
			report.Unavailable++
			rt.logger.Printf("search backfill: %s not searchable (%s)", name, reason)
		default:
			report.Failed++
			rt.logger.Printf("search backfill: %s failed (%s)", name, reason)
		}
	}
	return report, nil
}

type searchBackfillOutcome int

const (
	searchBackfillFailed searchBackfillOutcome = iota
	searchBackfillIndexed
	searchBackfillUnchanged
	searchBackfillUnavailable
)

func (rt *Runtime) backfillOneMeeting(
	ctx context.Context, target searchBackfillTarget, opusName string, indexed map[string]string,
) (searchBackfillOutcome, string) {
	job, err := rt.store.GetJob(ctx, target.JobID)
	if err != nil {
		return searchBackfillFailed, fmt.Sprintf("read job: %v", err)
	}
	delivered := ""
	if job.ArtifactOpusSHA256 != nil {
		delivered = strings.TrimSpace(*job.ArtifactOpusSHA256)
	}
	if delivered == "" {
		// Without the delivered digest there is no way to tell whether the local
		// bundle is the one that was published, and guessing is the failure this
		// whole check exists to prevent.
		return rt.recordUnavailable(ctx, opusName, searchBackfillReasonNoDigest)
	}
	// Already current: re-reading and re-writing identical rows is pure cost, and
	// a backfill is expected to be re-runnable.
	if existing, ok := indexed[opusName]; ok && existing != "" && existing == delivered {
		return searchBackfillUnchanged, ""
	}

	// The same whole-container digest the seal-to-publish chain uses, so the two
	// sides of this comparison are the same claim (digest.go).
	localDigest, err := fileSHA256(canonicalOpusPath(rt.cfg.WorkRoot, target.JobID))
	if err != nil {
		return rt.recordUnavailable(ctx, opusName, searchBackfillReasonNoBundle)
	}
	if !strings.EqualFold(localDigest, delivered) {
		// The promoted bundle is not the artifact that was delivered. See the
		// header: this is a rerun that built and then failed to publish.
		return rt.recordUnavailable(ctx, opusName, searchBackfillReasonStaleBundle)
	}

	transcript, err := readBundleTranscript(canonicalMeetingPath(rt.cfg.WorkRoot, target.JobID))
	if err != nil {
		return rt.recordUnavailable(ctx, opusName, searchBackfillReasonUnreadable)
	}
	rows := searchRowsFromSegments(bundleTranscriptSegments(transcript))
	if len(rows) == 0 {
		return rt.recordUnavailable(ctx, opusName, searchBackfillReasonNoSegments)
	}
	if err := rt.searchStore.ReplaceMeeting(ctx, opusName, delivered, searchRowSourceSegments, rows); err != nil {
		return searchBackfillFailed, fmt.Sprintf("write rows: %v", err)
	}
	return searchBackfillIndexed, ""
}

func (rt *Runtime) recordUnavailable(ctx context.Context, opusName, reason string) (searchBackfillOutcome, string) {
	if err := rt.searchStore.MarkUnavailable(ctx, opusName, reason); err != nil {
		return searchBackfillFailed, fmt.Sprintf("record %s: %v", reason, err)
	}
	return searchBackfillUnavailable, reason
}

// indexedDigests reports the artifact digest each meeting was last indexed
// from, so a re-run can skip what is already current.
func (s *searchStore) indexedDigests(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT opus_name, opus_sha256 FROM meeting_index WHERE state = ?`, searchStateIndexed)
	if err != nil {
		return nil, fmt.Errorf("read indexed digests: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, digest string
		if err := rows.Scan(&name, &digest); err != nil {
			return nil, fmt.Errorf("scan indexed digest: %w", err)
		}
		out[name] = digest
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read indexed digests: %w", err)
	}
	return out, nil
}
