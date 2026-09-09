package operator

import (
	"context"
	"database/sql"
	"errors"
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
	// The archive outlives the operator's volume, so a published recording whose
	// job row is gone is ordinary rather than exceptional.
	searchBackfillReasonNoJobRecord   = "job-record-missing"
	searchBackfillReasonArchiveUnread = "archive-recording-unreadable"
	// The job database could not be read at all — distinct from a row that is
	// genuinely absent, because only the second justifies the archive fallback.
	searchBackfillReasonJobStoreUnavailable = "job-store-unavailable"
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
	// Failed: could not even be recorded — no join key, a job store that could
	// not be read, or an index that rejected the write. Retryable.
	Failed int
	// Forgotten: rows dropped for meetings the archive no longer holds.
	Forgotten int
}

// backfillSearchIndex indexes each target that can be indexed safely.
//
// It never stops on one meeting's failure: an archive with a single unreadable
// bundle should still end up with every other meeting searchable.
func (rt *Runtime) backfillSearchIndex(ctx context.Context, targets []searchBackfillTarget, archive searchArchiveReader) (searchBackfillReport, error) {
	var report searchBackfillReport
	if rt.searchStore == nil {
		return report, fmt.Errorf("search index is not open")
	}
	indexed, err := rt.searchStore.indexedState(ctx)
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
		outcome, reason := rt.backfillOneMeeting(ctx, target, name, indexed, archive)
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

	forgotten, err := rt.forgetVanishedMeetings(ctx, targets)
	if err != nil {
		// Housekeeping: failing it must not discard a run that indexed
		// successfully.
		rt.logger.Printf("search backfill: could not prune vanished meetings (%v)", err)
	}
	report.Forgotten = forgotten
	return report, nil
}

// forgetVanishedMeetings drops index rows for meetings the archive no longer
// holds — the "converge" half of this command, which nothing else does.
//
// It changes nothing a caller sees: a recording they cannot read is already
// absent from their visibility scan. It matters because without it the index
// only ever grows, and a deleted meeting's words stay in the file for anyone
// who can run SQL against it.
//
// Guarded on a non-empty target list. An archive read that came back empty is
// indistinguishable from an archive that is genuinely empty, and erasing the
// whole index on a transient failure is exactly the kind of destructive
// convergence the D-631 assessment warned about.
func (rt *Runtime) forgetVanishedMeetings(ctx context.Context, targets []searchBackfillTarget) (int, error) {
	if len(targets) == 0 {
		return 0, nil
	}
	present := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if name := strings.TrimSpace(target.OpusName); name != "" {
			present[name] = struct{}{}
		}
	}
	known, err := rt.searchStore.indexedNames(ctx)
	if err != nil {
		return 0, err
	}
	forgotten := 0
	for _, name := range known {
		if _, still := present[name]; still {
			continue
		}
		if err := rt.searchStore.ForgetMeeting(ctx, name); err != nil {
			return forgotten, err
		}
		rt.logger.Printf("search backfill: %s is no longer in the archive; dropped from the index", name)
		forgotten++
	}
	return forgotten, nil
}

// indexedNames lists every meeting the index holds a record for, in any state,
// so convergence can tell which the archive has stopped carrying.
func (s *searchStore) indexedNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT opus_name FROM meeting_index`)
	if err != nil {
		return nil, fmt.Errorf("list indexed meetings: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan indexed meeting: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

type searchBackfillOutcome int

const (
	searchBackfillFailed searchBackfillOutcome = iota
	searchBackfillIndexed
	searchBackfillUnchanged
	searchBackfillUnavailable
)

func (rt *Runtime) backfillOneMeeting(
	ctx context.Context, target searchBackfillTarget, opusName string,
	indexed map[string]searchIndexedState, archive searchArchiveReader,
) (searchBackfillOutcome, string) {
	// The local bundle is preferred wherever it can be trusted: it carries the
	// producer's own segments and costs nothing to read. Anything that cannot be
	// trusted falls through to the archive rather than giving up, because the
	// recording is the one artifact that is definitely there.
	delivered, localReason := rt.deliveredDigestFor(ctx, target)
	if localReason == "" {
		if existing, ok := indexed[opusName]; ok && existing.digest != "" &&
			existing.digest == delivered && existing.source == searchRowSourceSegments {
			// Re-runnable by design: identical rows are not rewritten.
			//
			// The source is compared as well as the digest. Without it, a
			// meeting once indexed from the archive would report `unchanged`
			// forever and keep its coarse word rows even after the bundle came
			// back — the digest matches either way.
			return searchBackfillUnchanged, ""
		}
		outcome, reason, settled := rt.indexFromLocalBundle(ctx, target, opusName, delivered)
		if settled {
			return outcome, reason
		}
		// Carry the SPECIFIC reason the local copy could not be used. Collapsing
		// them here would record "bundle-newer-than-delivered" for a bundle that
		// is simply absent, which is the kind of plausible-but-wrong reason an
		// operator would chase.
		localReason = reason
	}

	if localReason == searchBackfillReasonJobStoreUnavailable {
		// Retryable, and not something to paper over by rebuilding from the
		// archive: the local bundle may be perfectly good and simply unreadable
		// right now.
		return searchBackfillFailed, localReason
	}
	if archive == nil {
		if _, wasIndexed := indexed[opusName]; wasIndexed {
			// No archive access, and the meeting holds rows from an earlier run.
			// Absence of the recording is not evidence those rows are wrong, and
			// recording it unavailable would DELETE them — so this is a failure
			// to verify, not a verdict.
			return searchBackfillFailed, localReason
		}
		// No archive access: record why the local copy could not be used, so the
		// meeting is known-unsearchable rather than merely absent.
		return rt.recordUnavailable(ctx, opusName, localReason)
	}
	words, digest, err := archive(ctx, opusName)
	if err != nil {
		// A failed READ is not a verdict on the meeting, and it must not become
		// one: recordUnavailable drops whatever rows the meeting already has, so
		// one timeout during a routine re-run would silently un-index a meeting
		// that was searchable a minute earlier — the destructive convergence
		// forgetVanishedMeetings refuses, arriving per meeting. Failed keeps the
		// rows and tells the operator to re-run.
		return searchBackfillFailed, fmt.Sprintf("%s: %v", searchBackfillReasonArchiveUnread, err)
	}
	if existing, ok := indexed[opusName]; ok && existing.digest != "" && existing.digest == digest {
		return searchBackfillUnchanged, ""
	}
	rows := deriveSearchRowsFromWords(words)
	if len(rows) == 0 {
		return rt.recordUnavailable(ctx, opusName, searchBackfillReasonNoSegments)
	}
	// row_source records these as the coarser kind, so an answer built on them
	// never implies the precision a segment row would carry.
	if err := rt.searchStore.ReplaceMeeting(ctx, opusName, digest, searchRowSourceWords, rows); err != nil {
		return searchBackfillFailed, fmt.Sprintf("write rows: %v", err)
	}
	return searchBackfillIndexed, ""
}

// deliveredDigestFor reports the digest of the artifact this job delivered, or
// the reason the local record cannot say.
func (rt *Runtime) deliveredDigestFor(ctx context.Context, target searchBackfillTarget) (string, string) {
	job, err := rt.store.GetJob(ctx, target.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		// The archive outlives the operator's volume, so a published recording
		// with no job row is ordinary rather than exceptional. Named, not
		// counted as a failure.
		return "", searchBackfillReasonNoJobRecord
	}
	if err != nil {
		// Anything else is the job database being unavailable, and it must NOT
		// look like an absent row. The command's own usage warns that a publish
		// in flight fails this read with "database is locked" — and treating
		// that as "no job record" would send every meeting down the archive
		// fallback, re-indexing a whole archive at the coarser granularity and
		// overwriting good segment rows on the way.
		return "", searchBackfillReasonJobStoreUnavailable
	}
	if job.ArtifactOpusSHA256 == nil || strings.TrimSpace(*job.ArtifactOpusSHA256) == "" {
		return "", searchBackfillReasonNoDigest
	}
	return strings.TrimSpace(*job.ArtifactOpusSHA256), ""
}

// indexFromLocalBundle indexes from current/, or reports that it could not.
//
// settled is false when the caller should try the archive instead, and the
// returned reason then says WHY the local copy was unusable — the bundle is
// missing, unreadable, or is not the artifact that was delivered. settled is
// true when the meeting is decided either way: indexed, or genuinely holding
// no speech.
func (rt *Runtime) indexFromLocalBundle(
	ctx context.Context, target searchBackfillTarget, opusName, delivered string,
) (searchBackfillOutcome, string, bool) {
	// The same whole-container digest the seal-to-publish chain uses, so both
	// sides of this comparison are the same claim (digest.go).
	localDigest, err := fileSHA256(canonicalOpusPath(rt.cfg.WorkRoot, target.JobID))
	if err != nil {
		return 0, searchBackfillReasonNoBundle, false
	}
	if !strings.EqualFold(localDigest, delivered) {
		// A rerun that built and then failed to publish. The archive holds what
		// was actually delivered, so falling through to it does not work around
		// the mismatch — it resolves it.
		return 0, searchBackfillReasonStaleBundle, false
	}
	transcript, err := readBundleTranscript(canonicalMeetingPath(rt.cfg.WorkRoot, target.JobID))
	if err != nil {
		return 0, searchBackfillReasonUnreadable, false
	}
	rows := searchRowsFromSegments(bundleTranscriptSegments(transcript))
	if len(rows) == 0 {
		// The bundle parsed and holds no speech. The archive would say the same,
		// so this is settled rather than worth a download.
		outcome, reason := rt.recordUnavailable(ctx, opusName, searchBackfillReasonNoSegments)
		return outcome, reason, true
	}
	if err := rt.searchStore.ReplaceMeeting(ctx, opusName, delivered, searchRowSourceSegments, rows); err != nil {
		return searchBackfillFailed, fmt.Sprintf("write rows: %v", err), true
	}
	return searchBackfillIndexed, "", true
}

func (rt *Runtime) recordUnavailable(ctx context.Context, opusName, reason string) (searchBackfillOutcome, string) {
	if err := rt.searchStore.MarkUnavailable(ctx, opusName, reason); err != nil {
		return searchBackfillFailed, fmt.Sprintf("record %s: %v", reason, err)
	}
	return searchBackfillUnavailable, reason
}

// searchIndexedState is what a meeting was last indexed from: which artifact,
// and at which granularity. Both matter to a re-run — the digest says whether
// the content changed, the source says whether a better one is now available.
type searchIndexedState struct {
	digest string
	source string
}

// indexedState reports how each meeting was last indexed, so a re-run can skip
// what is already current and upgrade what is not.
func (s *searchStore) indexedState(ctx context.Context) (map[string]searchIndexedState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT opus_name, opus_sha256, row_source FROM meeting_index WHERE state = ?`, searchStateIndexed)
	if err != nil {
		return nil, fmt.Errorf("read indexed state: %w", err)
	}
	defer rows.Close()
	out := map[string]searchIndexedState{}
	for rows.Next() {
		var name string
		var state searchIndexedState
		if err := rows.Scan(&name, &state.digest, &state.source); err != nil {
			return nil, fmt.Errorf("scan indexed state: %w", err)
		}
		out[name] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read indexed state: %w", err)
	}
	return out, nil
}
