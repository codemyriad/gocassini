package operator

import (
	"context"
	"fmt"
	"strings"
)

// Backfill: making meetings published before the index existed searchable
// (D-623).
//
// It reads current/<job>.meeting, which is never pruned and carries the
// producer's segments; the published `.opus` carries only raw words (D-695).
//
// But current/ tracks the last attempt that BUILT, not the last that was
// DELIVERED, so a rerun that failed to publish leaves a transcript there that
// does not match the recording anyone can play. Every meeting is therefore
// checked against the archive's own record of what was delivered (see
// archiveDeliveredState for why the job database cannot serve), and a mismatch
// falls through to the archive, which resolves it.
//
// Every mark rewrites a delivered recording's OpusTags, and so its container
// digest (D-737); its audio digest does not move. Equal container digests stay
// the cheap proof; when they differ and the archive copy is in hand, the CLI
// compares the audio. Equal: the bundle is the delivered recording's, marks
// aside. Different: the rerun that never published, refused as before.

const (
	searchBackfillReasonNoBundle = "bundle-missing"
	// The archive holds the recording but records no checksum for it — an
	// upload from before deliveries carried one — so a local bundle cannot be
	// verified against it without the delivered bytes in hand.
	searchBackfillReasonNoDigest      = "delivered-digest-unknown"
	searchBackfillReasonStaleBundle   = "bundle-newer-than-delivered"
	searchBackfillReasonUnreadable    = "transcript-unreadable"
	searchBackfillReasonNoSegments    = "transcript-has-no-segments"
	searchBackfillReasonArchiveUnread = "archive-recording-unreadable"
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
func (rt *Runtime) backfillSearchIndex(ctx context.Context, targets []searchBackfillTarget, delivered searchDeliveredStateReader, archive searchArchiveReader) (searchBackfillReport, error) {
	var report searchBackfillReport
	if rt.searchStore == nil {
		return report, fmt.Errorf("search index is not open")
	}
	if delivered == nil {
		// Without the archive's own record there is no way to know what was
		// delivered, and guessing is the failure the whole check exists to stop.
		return report, fmt.Errorf("backfill needs the archive's delivered state")
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
		outcome, reason := rt.backfillOneMeeting(ctx, target, name, indexed, delivered, archive)
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
	indexed map[string]searchIndexedState, delivered searchDeliveredStateReader, archive searchArchiveReader,
) (searchBackfillOutcome, string) {
	// What was DELIVERED is the archive's record to give. The job database
	// cannot answer it — its digest is written at seal time, in the same step
	// that promotes current/, so the two move together across attempts and
	// comparing them proves nothing about delivery (archiveDeliveredState).
	deliveredDigest, exists, err := delivered(ctx, opusName)
	if err != nil {
		// A failure to ask, not a verdict: nothing is written, existing rows
		// stay, re-running is the fix.
		return searchBackfillFailed, fmt.Sprintf("read delivered state: %v", err)
	}
	if !exists {
		// The catalog names it but the leaf is gone — a deletion in flight, or
		// drift this run cannot judge. Convergence prunes it once the catalog
		// stops naming it; until then this too is a failure to verify.
		return searchBackfillFailed, "the catalog names it but the archive does not hold it"
	}
	existing, wasIndexed := indexed[opusName]

	if deliveredDigest != "" {
		if sameIndexedArtifact(existing, wasIndexed, deliveredDigest, searchRowSourceSegments) {
			// Re-runnable by design: identical rows are not rewritten. The
			// source is compared as well as the digest — without it, a meeting
			// once indexed from the archive would report `unchanged` forever
			// and keep its coarse word rows even after the bundle came back.
			return searchBackfillUnchanged, ""
		}
		// The local bundle is preferred wherever it can be trusted: it carries
		// the producer's own segments and costs nothing to read. No audio digest
		// yet — that needs the delivered bytes, which are only fetched below.
		outcome, reason, settled := rt.indexFromLocalBundle(ctx, target, opusName, deliveredDigest, "")
		if settled {
			return outcome, reason
		}
		if sameIndexedArtifact(existing, wasIndexed, deliveredDigest, searchRowSourceWords) {
			// Already built from this very artifact, and no better source has
			// appeared: nothing to download.
			return searchBackfillUnchanged, ""
		}
		// Carry the SPECIFIC reason the local copy could not be used.
		// Collapsing them would record "bundle-newer-than-delivered" for a
		// bundle that is simply absent, which is the kind of plausible-but-
		// wrong reason an operator would chase.
		return rt.indexFromArchive(ctx, target, opusName, existing, wasIndexed, archive, reason)
	}
	// A recording delivered before uploads carried checksums: present, but with
	// no recorded digest to verify the local bundle against. The delivered
	// bytes themselves are the only ground truth left.
	return rt.indexFromArchive(ctx, target, opusName, existing, wasIndexed, archive, searchBackfillReasonNoDigest)
}

// indexFromArchive settles a meeting from the published recording itself: the
// one artifact that is definitely there, and definitely what was delivered.
func (rt *Runtime) indexFromArchive(
	ctx context.Context, target searchBackfillTarget, opusName string,
	existing searchIndexedState, wasIndexed bool, archive searchArchiveReader, localReason string,
) (searchBackfillOutcome, string) {
	if archive == nil {
		if wasIndexed {
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
	read, release, err := archive(ctx, opusName)
	if err != nil {
		// A failed READ is not a verdict: recordUnavailable would drop the rows
		// the meeting already has, so one timeout would un-index a searchable
		// meeting. Failed keeps the rows and tells the operator to re-run.
		return searchBackfillFailed, fmt.Sprintf("%s: %v", searchBackfillReasonArchiveUnread, err)
	}
	defer release()
	digest := read.Digest
	if sameIndexedArtifact(existing, wasIndexed, digest, searchRowSourceSegments) {
		return searchBackfillUnchanged, ""
	}
	// The delivered bytes are in hand, which makes the local bundle verifiable
	// after all — by audio, if the archive copy has been marked since.
	if outcome, reason, settled := rt.indexFromLocalBundle(ctx, target, opusName, digest, read.Path); settled {
		return outcome, reason
	}
	if sameIndexedArtifact(existing, wasIndexed, digest, searchRowSourceWords) {
		return searchBackfillUnchanged, ""
	}
	rows := deriveSearchRowsFromWords(read.Words)
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

// sameIndexedArtifact reports whether the meeting's existing rows were built
// from this very artifact, at this granularity.
func sameIndexedArtifact(existing searchIndexedState, wasIndexed bool, digest, source string) bool {
	return wasIndexed && digest != "" && strings.EqualFold(existing.digest, digest) && existing.source == source
}

// indexFromLocalBundle indexes from current/, or reports that it could not.
//
// settled is false when the caller should try the archive instead, and reason
// then says why the local copy was unusable. settled is true when the meeting
// is decided either way: indexed, or genuinely holding no speech.
//
// delivered is the archive copy's container digest, and is what gets recorded,
// so the next run's one PROPFIND can compare against it. deliveredCopy is the
// archive copy on disk once it has been downloaded, "" before.
func (rt *Runtime) indexFromLocalBundle(
	ctx context.Context, target searchBackfillTarget, opusName, delivered, deliveredCopy string,
) (searchBackfillOutcome, string, bool) {
	localOpus := canonicalOpusPath(rt.cfg.WorkRoot, target.JobID)
	localDigest, err := fileSHA256(localOpus)
	if err != nil {
		return 0, searchBackfillReasonNoBundle, false
	}
	if !strings.EqualFold(localDigest, delivered) && !rt.sameOpusAudio(ctx, localOpus, deliveredCopy) {
		// A rerun that built and then failed to publish: different audio, not
		// merely different marks. The archive resolves it.
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

// sameOpusAudio reports whether two recordings carry the same audio digest: the
// same recording, whatever either copy's marks (D-737). Unknown is not a match,
// which leaves the bundle unverified, where it stood before marks existed.
func (rt *Runtime) sameOpusAudio(ctx context.Context, localOpus, deliveredCopy string) bool {
	if deliveredCopy == "" {
		return false
	}
	var digests [2]string
	for i, file := range []string{deliveredCopy, localOpus} {
		shown, err := runAnnotateShow(ctx, rt.cfg.CassiniBin, file)
		if err != nil {
			if rt.logger != nil {
				rt.logger.Printf("search backfill: cannot read the audio digest of %s, so the bundle stays unverified (%v)", file, err)
			}
			return false
		}
		digests[i] = strings.TrimSpace(shown.AudioOpusSHA256)
	}
	return digests[0] != "" && strings.EqualFold(digests[0], digests[1])
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
