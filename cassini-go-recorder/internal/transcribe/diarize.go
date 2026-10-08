package transcribe

import (
	"fmt"
	"math"
	"sort"
)

// SpeakerTurn is one diarizer turn on the meeting timeline. Speaker is the
// diarizer's own integer label: arbitrary, zero-based, meaningful only within
// one run. Turns of different speakers may overlap (overlapping speech).
type SpeakerTurn struct {
	StartMS int64
	EndMS   int64
	Speaker int
}

// DiarizationAssignment names the word-to-speaker rule so provenance records
// which rule produced a split. It is the Android rule (cassini-android
// Diarization.assign), kept identical so both producers agree on one recording.
const DiarizationAssignment = "largest union overlap; nearest turn in gaps; one speaker per word"

// normalizeTurns applies the Android post-processing to raw diarizer output
// given in seconds: drop non-finite times and negative speakers, convert to
// milliseconds, clamp to [0, durationMS] (when durationMS > 0) and drop empty
// turns. The result is sorted by (start, end, speaker).
func normalizeTurns(raw []rawTurn, durationMS int64) []SpeakerTurn {
	turns := make([]SpeakerTurn, 0, len(raw))
	for _, r := range raw {
		if r.Speaker < 0 || !finite(r.Start) || !finite(r.End) {
			continue
		}
		start := clampMS(int64(r.Start*1000), durationMS)
		end := clampMS(int64(r.End*1000), durationMS)
		if end <= start {
			continue
		}
		turns = append(turns, SpeakerTurn{StartMS: start, EndMS: end, Speaker: r.Speaker})
	}
	sortTurns(turns)
	return turns
}

type rawTurn struct {
	Start, End float64
	Speaker    int
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func clampMS(v, durationMS int64) int64 {
	if v < 0 {
		return 0
	}
	if durationMS > 0 && v > durationMS {
		return durationMS
	}
	return v
}

func sortTurns(turns []SpeakerTurn) {
	sort.SliceStable(turns, func(i, j int) bool {
		a, b := turns[i], turns[j]
		if a.StartMS != b.StartMS {
			return a.StartMS < b.StartMS
		}
		if a.EndMS != b.EndMS {
			return a.EndMS < b.EndMS
		}
		return a.Speaker < b.Speaker
	})
}

// unionTurns merges each speaker's overlapping or touching turns, then sorts
// everything by (start, end, speaker) so ties never depend on native order.
func unionTurns(turns []SpeakerTurn) []SpeakerTurn {
	bySpeaker := map[int][]SpeakerTurn{}
	var speakers []int
	for _, t := range turns {
		if t.StartMS < 0 || t.EndMS <= t.StartMS || t.Speaker < 0 {
			continue
		}
		if _, ok := bySpeaker[t.Speaker]; !ok {
			speakers = append(speakers, t.Speaker)
		}
		bySpeaker[t.Speaker] = append(bySpeaker[t.Speaker], t)
	}
	var union []SpeakerTurn
	for _, speaker := range speakers {
		intervals := bySpeaker[speaker]
		sortTurns(intervals)
		var merged []SpeakerTurn
		for _, t := range intervals {
			if n := len(merged); n > 0 && t.StartMS <= merged[n-1].EndMS {
				if t.EndMS > merged[n-1].EndMS {
					merged[n-1].EndMS = t.EndMS
				}
				continue
			}
			merged = append(merged, t)
		}
		union = append(union, merged...)
	}
	sortTurns(union)
	return union
}

// assignWordsToTurns returns, for each word, the diarizer speaker it belongs
// to: the speaker with the largest summed overlap; for a word no turn overlaps
// (a gap, or a zero-length word), the speaker of the nearest turn. Ties go to
// the turn that comes first in (start, end, speaker) order. It returns nil
// when there are no usable turns.
func assignWordsToTurns(words []Word, turns []SpeakerTurn) []int {
	union := unionTurns(turns)
	if len(union) == 0 {
		return nil
	}
	out := make([]int, len(words))
	for i, w := range words {
		// Insertion-ordered sums so a tie keeps the first speaker seen in union
		// order, matching Kotlin's maxByOrNull over a LinkedHashMap.
		var order []int
		sums := map[int]int64{}
		for _, t := range union {
			overlap := min(w.EndMS, t.EndMS) - max(w.StartMS, t.StartMS)
			if overlap <= 0 {
				continue
			}
			if _, ok := sums[t.Speaker]; !ok {
				order = append(order, t.Speaker)
			}
			sums[t.Speaker] += overlap
		}
		if len(order) > 0 {
			best := order[0]
			for _, s := range order[1:] {
				if sums[s] > sums[best] {
					best = s
				}
			}
			out[i] = best
			continue
		}
		best := union[0]
		bestGap := turnGap(w, best)
		for _, t := range union[1:] {
			if g := turnGap(w, t); g < bestGap {
				best, bestGap = t, g
			}
		}
		out[i] = best.Speaker
	}
	return out
}

func turnGap(w Word, t SpeakerTurn) int64 {
	return max(t.StartMS-w.EndMS, w.StartMS-t.EndMS, 0)
}

// SubSpeakerID names the n-th (1-based) voice found on a shared device's
// stream. It is derived from the parent id and the voice's position, so
// re-applying the same split to the same audio reproduces the same ids and a
// name given to one survives the rebuild.
func SubSpeakerID(parentID string, n int) string {
	return fmt.Sprintf("%s~%d", parentID, n)
}

// SplitResult describes one stream split into voices.
type SplitResult struct {
	ParentID string
	// SubSpeakerIDs lists the new ids in order of first spoken word.
	SubSpeakerIDs []string
	// WordsPerSpeaker counts words per new id, aligned with SubSpeakerIDs.
	WordsPerSpeaker []int
	// TurnCount and SpeakerCount describe the diarizer output used.
	TurnCount    int
	SpeakerCount int
}

// SplitSpeakerSegments reassigns every word of parentID to a voice found by
// the diarizer, numbering voices by first spoken word, and rebuilds the
// segments so each voice change starts a new turn. Words of every other
// speaker, and every word's text, timing and attribution fields, are kept
// exactly. With no usable turns or no words for parentID the input is returned
// unchanged and the result lists no sub-speakers.
func SplitSpeakerSegments(segments []Segment, parentID string, turns []SpeakerTurn) ([]Segment, SplitResult) {
	result := SplitResult{ParentID: parentID, TurnCount: len(turns), SpeakerCount: countTurnSpeakers(turns)}
	var parentWords []Word
	var perSpeaker [][]Segment
	for _, seg := range segments {
		if seg.SpeakerID == parentID && len(seg.Words) > 0 {
			parentWords = append(parentWords, seg.Words...)
			continue
		}
		// Other speakers, and text-only legacy segments that have no word
		// timings to split by, pass through unchanged.
		perSpeaker = append(perSpeaker, []Segment{seg})
	}
	if len(parentWords) == 0 {
		return segments, result
	}
	sort.SliceStable(parentWords, func(i, j int) bool { return parentWords[i].StartMS < parentWords[j].StartMS })
	assigned := assignWordsToTurns(parentWords, turns)
	if assigned == nil {
		return segments, result
	}

	ordinal := map[int]int{}
	perVoice := map[int][]Word{}
	for i, speaker := range assigned {
		if _, ok := ordinal[speaker]; !ok {
			ordinal[speaker] = len(ordinal) + 1
			result.SubSpeakerIDs = append(result.SubSpeakerIDs, SubSpeakerID(parentID, ordinal[speaker]))
			result.WordsPerSpeaker = append(result.WordsPerSpeaker, 0)
		}
		n := ordinal[speaker]
		perVoice[n] = append(perVoice[n], parentWords[i])
		result.WordsPerSpeaker[n-1]++
	}

	for n := 1; n <= len(ordinal); n++ {
		perSpeaker = append(perSpeaker, []Segment{{SpeakerID: SubSpeakerID(parentID, n), Words: perVoice[n]}})
	}
	return MergeAndSortSegments(perSpeaker), result
}

func countTurnSpeakers(turns []SpeakerTurn) int {
	seen := map[int]bool{}
	for _, t := range turns {
		seen[t.Speaker] = true
	}
	return len(seen)
}
