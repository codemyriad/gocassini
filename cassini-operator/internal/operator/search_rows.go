package operator

import (
	"fmt"
	"sort"
	"strings"
)

// What one indexable row is (D-623).
//
// A row is a TRANSCRIPT SEGMENT: one speaker's utterance, with its own id,
// speaker and bounds, passed through rather than re-cut. That is the unit a
// reader sees, so it is the unit a hit should name.
//
// WHERE SEGMENTS COME FROM, and where they do not:
//
//	transcript.words.v1.json in the attempt bundle   segments[]  YES
//	the published .opus                              words only  NO
//
// The bundle is what INGEST reads, so a meeting indexed at publish time gets
// segment rows. The published artifact carries the raw word transcript and
// nothing else — D-695 decoded all 128 published meetings in the archive and
// found every transcript role to be raw-asr, then deleted the readable-cleanup
// producer outright. The `display` role survives in the format, but its only
// producer is cassini-viewer's static-site export, which runs at publish, after
// the .opus has been sealed; nothing in the recorder or operator pipeline puts
// a display transcript into the artifact.
//
// So a REBUILD from the archive cannot recover segments. It gets words, and
// derives the coarser rows below. That asymmetry is real and is recorded per
// meeting in meeting_index.row_source rather than papered over: an answer can
// then say which kind of reference it is pointing at, instead of implying a
// word-derived window is an utterance.
//
// Do not "fix" this by re-deriving segments from words on rebuild. Three
// gap-and-cap rules already exist in this tree and disagree (pipeline
// 1500ms/60 words, meetings context 2200ms/96, the viewer one pseudo-segment
// per word), and the flattening drops empty-text words that the original
// boundaries counted — so a re-derivation is a fourth opinion, not a recovery.
// The honest fix, if segment-accurate rebuilds are ever wanted, is for the
// producer to carry segment boundaries in the portable manifest so they become
// data in the artifact.

const (
	// searchRowSourceSegments means rows came from the artifact's own segments.
	searchRowSourceSegments = "segments"
	// searchRowSourceWords means the rows were derived from word timings because
	// no segmentation was available — a rebuild from the published archive, or a
	// bundle that carries none. Coarser, and recorded as such so a result can be
	// honest about what it is pointing at.
	searchRowSourceWords = "words"
)

// searchTranscriptSegment is one segment of the bundle's word transcript.
type searchTranscriptSegment struct {
	ID        string
	SpeakerID string
	StartMS   int64
	EndMS     int64
	Text      string
}

// searchTranscriptWord is one word of the raw transcript, used only for the
// fallback derivation.
//
// SpeakerID is the speaker's opaque id, never their label. Labels bake in real
// names, and putting names into the postings would turn cross-meeting search
// into a people-tracking tool over the whole corpus.
type searchTranscriptWord struct {
	SpeakerID string
	StartMS   int64
	EndMS     int64
	Text      string
}

// searchRow is one indexable row.
type searchRow struct {
	// SegmentID names the row. For a segment row it is the producer's own id,
	// so a hit refers to the same unit the viewer renders. For a derived row it
	// is a synthetic, deterministic id — see deriveSearchRowsFromWords.
	SegmentID string
	StartMS   int64
	EndMS     int64
	SpeakerID string
	Text      string
}

// searchRowsFromSegments maps the bundle's transcript segments to rows.
//
// A straight mapping, deliberately: the producer owns the segmentation and this
// code has no business re-cutting it. Segments with no text are dropped — they
// cannot be matched and would only ever be a reference to nothing.
func searchRowsFromSegments(segments []searchTranscriptSegment) []searchRow {
	rows := make([]searchRow, 0, len(segments))
	for index, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		id := strings.TrimSpace(segment.ID)
		if id == "" {
			// An artifact whose segments carry no ids still indexes; the
			// position is stable for a given artifact, and the artifact is
			// immutable, so this is reproducible on a rebuild.
			id = fmt.Sprintf("seg_%06d", index)
		}
		start := segment.StartMS
		if start < 0 {
			start = 0
		}
		end := segment.EndMS
		if end < start {
			end = start
		}
		rows = append(rows, searchRow{
			SegmentID: id,
			StartMS:   start,
			EndMS:     end,
			SpeakerID: segment.SpeakerID,
			Text:      text,
		})
	}
	sortSearchRows(rows)
	return rows
}

// The fallback derivation, for rows built where no segmentation is available —
// principally a rebuild from the published archive.
//
// Words are grouped into OVERLAPPING wall-clock windows rather than cut into
// utterances, because cutting would mean choosing a gap-and-cap rule and three
// already exist in this tree that disagree. A window's membership is a pure
// function of each word's own startMs, so it needs no rule and no neighbours.
//
// Overlap costs twice the rows and buys the thing disjoint buckets lose: any
// two words less than one stride apart share a window, so a phrase cannot fall
// through a boundary.
const (
	searchWindowWidthMS  = 30_000
	searchWindowStrideMS = 15_000
)

// deriveSearchRowsFromWords builds fallback rows.
//
// The synthetic id encodes the window and speaker, so it is stable across
// rebuilds of the same artifact and legible when debugging a stray hit.
// StartMS/EndMS are the SPEECH inside the window, never the window's own
// bounds: citing the bounds would send a reader to a thirty-second haystack
// beginning wherever the arithmetic landed.
func deriveSearchRowsFromWords(words []searchTranscriptWord) []searchRow {
	type bucketKey struct {
		index   int64
		speaker string
	}
	type bucketValue struct {
		texts   []string
		startMS int64
		endMS   int64
	}
	buckets := map[bucketKey]*bucketValue{}

	for _, word := range words {
		text := strings.TrimSpace(word.Text)
		if text == "" {
			continue
		}
		start := word.StartMS
		if start < 0 {
			// A negative stamp cannot be placed on the meeting's clock, and
			// folding it to zero would assert it was said at the start.
			continue
		}
		end := word.EndMS
		if end < start {
			end = start
		}
		last := start / searchWindowStrideMS
		for index := last - 1; index <= last; index++ {
			if index < 0 {
				continue
			}
			key := bucketKey{index: index, speaker: word.SpeakerID}
			value := buckets[key]
			if value == nil {
				value = &bucketValue{startMS: start, endMS: end}
				buckets[key] = value
			}
			value.texts = append(value.texts, text)
			if start < value.startMS {
				value.startMS = start
			}
			if end > value.endMS {
				value.endMS = end
			}
		}
	}

	rows := make([]searchRow, 0, len(buckets))
	for key, value := range buckets {
		rows = append(rows, searchRow{
			SegmentID: fmt.Sprintf("w_%09d_%s", key.index*searchWindowStrideMS, key.speaker),
			StartMS:   value.startMS,
			EndMS:     value.endMS,
			SpeakerID: key.speaker,
			Text:      strings.Join(value.texts, " "),
		})
	}
	sortSearchRows(rows)
	return rows
}

// sortSearchRows puts rows in a total, reproducible order. Map iteration and
// producer order are both unreliable, and "delete the index and rebuild"
// cannot be checked against anything without a stable order.
func sortSearchRows(rows []searchRow) {
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.StartMS != right.StartMS {
			return left.StartMS < right.StartMS
		}
		if left.EndMS != right.EndMS {
			return left.EndMS < right.EndMS
		}
		if left.SpeakerID != right.SpeakerID {
			return left.SpeakerID < right.SpeakerID
		}
		return left.SegmentID < right.SegmentID
	})
}
