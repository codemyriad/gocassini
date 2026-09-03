package operator

import (
	"fmt"
	"sort"
	"strings"
)

// What one indexable row is (D-623).
//
// A row is a TRANSCRIPT SEGMENT: one speaker's utterance, with the producer's
// own id and bounds. Not a re-derivation, and not a synthetic time bucket —
// the published `.opus` carries the readable transcript as its own payload
// alongside the raw one, so segments are data in the artifact:
//
//	published .opus
//	  ├─ transcripts[]         -> transcript.words.v1  (one item per word)
//	  └─ readableTranscripts[] -> segments[] {id, speaker, startMs, endMs, text}
//
// That is the same payload the viewer loads to render a meeting opened from a
// portable file, which is what makes a hit name the unit a reader will actually
// see. It is also what makes a rebuild a rebuild: the index reads the archive
// and gets the producer's segments back, rather than guessing where they were.
//
// The fallback below exists only because a readable transcript is conditional
// (writeReadableArtifacts decides), so some meetings carry none. Those are
// indexed from words instead, and say so, rather than being left unsearchable.
const (
	// searchRowSourceSegments means rows came from the artifact's own segments.
	searchRowSourceSegments = "segments"
	// searchRowSourceWords means the meeting had no readable transcript and its
	// rows were derived from word timings. Coarser, and recorded as such so a
	// result can be honest about what it is pointing at.
	searchRowSourceWords = "words"
)

// searchReadableSegment is one segment of the artifact's readable transcript.
type searchReadableSegment struct {
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

// searchRowsFromSegments maps the artifact's readable segments to rows.
//
// A straight mapping, deliberately: the producer owns the segmentation and this
// code has no business re-cutting it. Segments with no text are dropped — they
// cannot be matched and would only ever be a reference to nothing.
func searchRowsFromSegments(segments []searchReadableSegment) []searchRow {
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

// The fallback derivation, for a meeting with no readable transcript.
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
