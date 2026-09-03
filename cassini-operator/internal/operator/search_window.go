package operator

import (
	"sort"
	"strings"
)

// Windowing: how a transcript becomes indexable rows (D-623).
//
// Rows are keyed on WALL-CLOCK WINDOWS, not on transcript segments. That is the
// one structural decision in the whole index, and it is worth stating why,
// because segments are the obvious choice and they are wrong here.
//
// The published `.opus` has no segments. flattenPortableTranscriptItems
// explodes every segment into one item per word, keeping only speaker, start,
// end and text — segment ids, boundaries and segment text are gone. Three
// rebuilders already exist and disagree about how to put them back: the
// pipeline breaks on a 1500 ms gap or 60 words, `meetings context` on 2200 ms
// or 96, and the viewer makes one pseudo-segment per word. An index keyed on a
// fourth derivation would own a fact nothing else records.
//
// Worse, gap-and-cap rules are NON-LOCAL. Inserting one word shifts every later
// boundary in that speaker's run, so a re-transcription silently moves every
// citation after the change:
//
//	segments      one extra word at 00:03:12 shifts every later boundary.
//	              A reference saved as t=00:14:32 now reads 00:14:29, and
//	              nothing records that the rule moved.
//
//	wall clock    the word lands in the window containing 00:03:12.
//	              Every other window's start_ms is byte-identical.
//
// A word's window membership is a pure function of its own startMs, which the
// artifact carries verbatim. So the row set is reproducible from either source
// and perturbs only locally — which is what gives "delete the index and rebuild
// it produces identical references" any force at all.
const (
	// searchWindowWidthMS is how much speech one row covers.
	searchWindowWidthMS = 30_000
	// searchWindowStrideMS is how far apart consecutive windows start. Windows
	// OVERLAP, at twice the row count, so a phrase cannot fall through a
	// boundary: any two words less than one stride apart share a window, and
	// therefore any phrase a caller might search for is contiguous in at least
	// one row. Disjoint buckets would lose exactly the phrases that straddle a
	// boundary, and lose them silently.
	searchWindowStrideMS = 15_000
)

// searchTranscriptWord is one word of a transcript, as the portable artifact
// carries it.
//
// SpeakerID is the speaker's opaque id, never their label. Labels bake in real
// names, and putting names into the postings would turn cross-meeting search
// into a people-tracking tool over the whole corpus. This deliberately diverges
// from the viewer's in-meeting searchSegments, which concatenates the label
// into its haystack — that search is scoped to one meeting the caller already
// opened, and this one is not.
type searchTranscriptWord struct {
	SpeakerID string
	StartMS   int64
	EndMS     int64
	Text      string
}

// searchWindow is one indexable row: one speaker's words inside one bucket.
//
// The distinction between BucketMS and StartMS/EndMS is the point, so it is
// worth stating rather than leaving to be inferred.
//
// BucketMS is the row's IDENTITY — which bucket this is — and it is a pure
// function of each word's own startMs. It is what makes the row set stable: a
// re-transcription that inserts a word changes only the buckets that word falls
// in, and every other bucket keeps the identity it had.
//
// StartMS and EndMS are the row's REFERENCE — when someone actually spoke
// inside it. They are the first word's start and the last word's end, not the
// bucket's bounds, because the bucket's bounds are an artifact of the indexing
// scheme and reporting them would send a reader to a 30-second haystack that
// begins wherever the arithmetic happened to land. A hit should say "they said
// this at 00:14:32", not "somewhere in the half minute from 00:15:00".
type searchWindow struct {
	// BucketMS identifies the bucket: index * searchWindowStrideMS.
	BucketMS int64
	// StartMS and EndMS bound the actual speech in this row.
	StartMS   int64
	EndMS     int64
	SpeakerID string
	Text      string
}

// buildSearchWindows turns a meeting's words into its rows.
//
// Pure, and deliberately so: it is the function that has to produce identical
// output from the attempt bundle and from a rebuild off the archive, or the
// index's disposability is a claim rather than a property.
//
// Words are bucketed by their own startMs, split by speaker, and emitted in
// (start, speaker) order so the row set is deterministic rather than dependent
// on map iteration. A word with no text contributes nothing; a word with no
// speaker is kept under the empty speaker id, because "somebody said this here"
// is still a true and searchable reference.
func buildSearchWindows(words []searchTranscriptWord) []searchWindow {
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
			// A negative stamp cannot be placed on the meeting's clock. Folding
			// it to zero would assert it was said at the start, which is a claim
			// the artifact does not make.
			continue
		}
		end := word.EndMS
		if end < start {
			// A missing or nonsensical end is not a reason to drop a word that
			// has a usable start; the reference degrades to a point in time.
			end = start
		}
		// A word belongs to the bucket its startMs falls in, and to the previous
		// one, which still covers it because buckets are twice the stride wide.
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
			// Words arrive in whatever order the caller supplies, so the bounds
			// are widened rather than assumed to be first-and-last.
			if start < value.startMS {
				value.startMS = start
			}
			if end > value.endMS {
				value.endMS = end
			}
		}
	}

	windows := make([]searchWindow, 0, len(buckets))
	for key, value := range buckets {
		windows = append(windows, searchWindow{
			BucketMS:  key.index * searchWindowStrideMS,
			StartMS:   value.startMS,
			EndMS:     value.endMS,
			SpeakerID: key.speaker,
			Text:      strings.Join(value.texts, " "),
		})
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].BucketMS != windows[j].BucketMS {
			return windows[i].BucketMS < windows[j].BucketMS
		}
		return windows[i].SpeakerID < windows[j].SpeakerID
	})
	return windows
}
