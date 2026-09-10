package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Publish-time ingest (D-623): a meeting becomes searchable when it is
// delivered, and not before.
//
// TWO SOURCES, AND ONLY ONE OF THEM IS RIGHT
//
// Ingest reads the ATTEMPT bundle — runs/<job>--attempt-NNN.meeting — never
// current/<job>.meeting, and the difference is not pedantry.
// promoteMeetingBundle runs in the BUILD worker, so `current/` tracks the last
// attempt that BUILT, not the last that was DELIVERED. A rerun whose build
// succeeds and whose seal or publish then fails leaves `current/` holding
// attempt N+1's transcript while Nextcloud still serves attempt N's `.opus`.
// Indexing that would have search cite words that are not in the recording
// anyone can actually play — a wrong answer delivered with full confidence.
//
// At publish time the attempt bundle is unambiguous: it is the one that was
// just packed and delivered.
//
// THE JOIN KEY IS TAKEN FROM THE CATALOG, NOT COMPOSED
//
// The visibility scan every query intersects against returns `.opus`
// BASENAMES, so a row must carry that exact string. It is read from the
// delivered catalog entry's audioPath rather than built from the job id: the
// catalog id, the job id and the packed filename coincide by convention only,
// and a composed name that drifts would match nothing and fail silently — every
// caller would simply never see that meeting in a result.
//
// FAILURE IS NON-FATAL BUT NEVER SILENT
//
// A publish must not fail because the index is unwritable. But a failure that
// only logs is worse than useless: the meeting keeps whatever row it had,
// coverage still reports it, and a search answers "no match in the 12 meetings
// you can read" about a transcript that was never indexed. So a failed ingest
// records state='unavailable' with a reason, and the meeting drops out of the
// covered count — a partial answer instead of a confident false negative.
//
// There is no operator switch for this yet. Adding one means declaring it in
// appinfo/info.xml (AppAPI silently drops undeclared env vars), which is
// config surface for a capability nothing can use; it belongs with the slice
// that makes search reachable.

// searchIngestReason values are recorded on meeting_index.reason so an operator
// can tell the failures apart without reading logs.
const (
	searchIngestReasonNoCatalogEntry = "no-catalog-entry"
	searchIngestReasonNoTranscript   = "no-transcript"
	searchIngestReasonUnreadable     = "transcript-unreadable"
	searchIngestReasonNoSegments     = "transcript-has-no-segments"
)

// searchIngestBundleTranscript is the shape ingest needs out of the bundle's
// transcript.words.v1.json. Deliberately a subset: this code reads four fields
// per segment and normalises none of the rest.
type searchIngestBundleTranscript struct {
	Version  string `json:"version"`
	Segments []struct {
		ID      string `json:"id"`
		Speaker string `json:"speaker"`
		StartMS int64  `json:"startMs"`
		EndMS   int64  `json:"endMs"`
		Text    string `json:"text"`
		Words   []struct {
			StartMS int64  `json:"startMs"`
			EndMS   int64  `json:"endMs"`
			Text    string `json:"text"`
		} `json:"words"`
	} `json:"segments"`
}

// indexPublishedMeeting makes one delivered meeting searchable.
//
// Returns an error only so the caller can log it; the caller must not fail the
// publish on it. A recoverable failure has already been recorded as
// `unavailable` by the time this returns.
func (rt *Runtime) indexPublishedMeeting(ctx context.Context, task publishTask, attemptSiteDir string) error {
	if rt.searchStore == nil {
		return nil
	}
	bundleDir := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)

	opusName, err := deliveredOpusName(attemptSiteDir, task.JobID)
	if err != nil {
		// Without the join key there is nothing to record the failure AGAINST —
		// meeting_index is keyed by it. Nothing is written, and the meeting is
		// simply absent from coverage rather than counted as searchable.
		return fmt.Errorf("resolve join key: %w", err)
	}

	transcript, err := readBundleTranscript(bundleDir)
	if err != nil {
		reason := searchIngestReasonUnreadable
		if os.IsNotExist(err) {
			reason = searchIngestReasonNoTranscript
		}
		if markErr := rt.searchStore.MarkUnavailable(ctx, opusName, reason); markErr != nil {
			return fmt.Errorf("%w (and recording it failed: %v)", err, markErr)
		}
		return err
	}

	rows := searchRowsFromSegments(bundleTranscriptSegments(transcript))
	if len(rows) == 0 {
		// A transcript that parsed but yielded nothing indexable is a real
		// state, not an error: a silent recording has no words. Recorded as
		// unavailable so it is not counted as searched.
		if err := rt.searchStore.MarkUnavailable(ctx, opusName, searchIngestReasonNoSegments); err != nil {
			return fmt.Errorf("record empty transcript: %w", err)
		}
		return nil
	}

	if err := rt.searchStore.ReplaceMeeting(ctx, opusName, task.OpusSHA256, searchRowSourceSegments, rows); err != nil {
		// Best-effort: try to leave the meeting marked unavailable rather than
		// carrying stale rows from a previous attempt.
		if markErr := rt.searchStore.MarkUnavailable(ctx, opusName, searchIngestReasonUnreadable); markErr != nil {
			return fmt.Errorf("%w (and recording it failed: %v)", err, markErr)
		}
		return err
	}
	rt.logger.Printf("search index updated id=%s attempt=%d opus=%s rows=%d source=%s",
		task.JobID, task.AttemptNumber, opusName, len(rows), searchRowSourceSegments)
	return nil
}

// deliveredOpusName reads the join key out of the attempt site's catalog.
//
// The site holds exactly one meeting — the exporter writes one entry per
// publish — so the job id is used only to prefer the right entry if that ever
// stops being true, never to construct the name.
func deliveredOpusName(attemptSiteDir, jobID string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(attemptSiteDir, "catalog.json"))
	if err != nil {
		return "", fmt.Errorf("read attempt catalog: %w", err)
	}
	var catalog struct {
		Meetings []struct {
			ID           string `json:"id"`
			AudioPath    string `json:"audioPath"`
			ArtifactPath string `json:"artifactPath"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return "", fmt.Errorf("parse attempt catalog: %w", err)
	}
	if len(catalog.Meetings) == 0 {
		return "", fmt.Errorf("attempt catalog names no meeting")
	}
	chosen := catalog.Meetings[0]
	for _, entry := range catalog.Meetings {
		if strings.TrimSpace(entry.ID) == strings.TrimSpace(jobID) {
			chosen = entry
			break
		}
	}
	ref := strings.TrimSpace(chosen.AudioPath)
	if ref == "" {
		// A directory-shaped legacy entry has no basename any visibility scan
		// can return, so it could only ever be indexed unreachably. Refuse it
		// rather than index a meeting no caller can be granted.
		return "", fmt.Errorf("catalog entry %q has no audioPath", chosen.ID)
	}
	name := path.Base(filepath.ToSlash(ref))
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("catalog entry %q has an unusable audioPath %q", chosen.ID, ref)
	}
	return name, nil
}

// readBundleTranscript loads the attempt bundle's word transcript.
func readBundleTranscript(bundleDir string) (searchIngestBundleTranscript, error) {
	raw, err := os.ReadFile(filepath.Join(bundleDir, "transcript.words.v1.json"))
	if err != nil {
		return searchIngestBundleTranscript{}, err
	}
	var transcript searchIngestBundleTranscript
	if err := json.Unmarshal(raw, &transcript); err != nil {
		return searchIngestBundleTranscript{}, fmt.Errorf("parse bundle transcript: %w", err)
	}
	return transcript, nil
}

// bundleTranscriptSegments converts the decoded bundle into the row builder's
// input, filling a segment's bounds from its own words where the segment does
// not carry usable ones.
func bundleTranscriptSegments(transcript searchIngestBundleTranscript) []searchTranscriptSegment {
	segments := make([]searchTranscriptSegment, 0, len(transcript.Segments))
	for _, segment := range transcript.Segments {
		start, end := segment.StartMS, segment.EndMS
		if end <= start && len(segment.Words) > 0 {
			// Widen from the words rather than emitting a zero-length reference:
			// a segment that spans real speech should cite it.
			start, end = segment.Words[0].StartMS, segment.Words[0].EndMS
			for _, word := range segment.Words {
				if word.StartMS < start {
					start = word.StartMS
				}
				if word.EndMS > end {
					end = word.EndMS
				}
			}
		}
		segments = append(segments, searchTranscriptSegment{
			ID:        segment.ID,
			SpeakerID: segment.Speaker,
			StartMS:   start,
			EndMS:     end,
			Text:      segment.Text,
		})
	}
	return segments
}
