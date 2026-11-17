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
// stream. n is the voice's rank by its first turn in the diarizer output, so
// it depends only on the stored turns: re-applying a split after the words
// were re-transcribed, or after the split was undone, reproduces the same ids
// and a name given to one stays with the same voice. Participant ids never
// contain "~" (speakerIDFromLabel), so the parent is always recoverable.
func SubSpeakerID(parentID string, n int) string {
	return fmt.Sprintf("%s~%d", parentID, n)
}

// voiceOrdinals ranks diarizer speakers by their first turn (start, end,
// speaker), 1-based.
func voiceOrdinals(turns []SpeakerTurn) map[int]int {
	sorted := append([]SpeakerTurn(nil), turns...)
	sortTurns(sorted)
	ordinal := map[int]int{}
	for _, t := range sorted {
		if t.StartMS < 0 || t.EndMS <= t.StartMS || t.Speaker < 0 {
			continue
		}
		if _, ok := ordinal[t.Speaker]; !ok {
			ordinal[t.Speaker] = len(ordinal) + 1
		}
	}
	return ordinal
}

// SplitResult describes one stream split into voices.
type SplitResult struct {
	ParentID string
	// SubSpeakerIDs lists the ids that received words, by voice number. A
	// voice the diarizer found but no word landed on keeps its number and is
	// left out.
	SubSpeakerIDs []string
	// WordsPerSpeaker counts words per id, aligned with SubSpeakerIDs.
	WordsPerSpeaker []int
	// TurnCount and SpeakerCount describe the diarizer output used.
	TurnCount    int
	SpeakerCount int
}

// SplitSpeakerSegments reassigns every word of parentID to a voice found by
// the diarizer and rebuilds the segments so each voice change starts a new
// turn. Words of every other speaker, and every word's text, timing and
// attribution fields, are kept exactly. With no usable turns or no words for
// parentID the input is returned unchanged and the result lists no voices.
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

	ordinal := voiceOrdinals(turns)
	perVoice := map[int][]Word{}
	for i, speaker := range assigned {
		n := ordinal[speaker]
		perVoice[n] = append(perVoice[n], parentWords[i])
	}
	for n := 1; n <= len(ordinal); n++ {
		if len(perVoice[n]) == 0 {
			continue
		}
		id := SubSpeakerID(parentID, n)
		result.SubSpeakerIDs = append(result.SubSpeakerIDs, id)
		result.WordsPerSpeaker = append(result.WordsPerSpeaker, len(perVoice[n]))
		perSpeaker = append(perSpeaker, []Segment{{SpeakerID: id, Words: perVoice[n]}})
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

// DiarizationProvenance records whole-recording speaker separation on a
// single-track build's speech-to-text provenance (x-speakerDiarization).
// Counts, ids and hashes only: no audio or biometric voice data.
type DiarizationProvenance struct {
	Backend          string  `json:"backend"`
	Model            string  `json:"model"`
	ModelSHA256      string  `json:"modelSha256"`
	MinDurationOn    float64 `json:"minDurationOn"`
	MinDurationOff   float64 `json:"minDurationOff"`
	Assignment       string  `json:"assignment"`
	SourceSeparation bool    `json:"sourceSeparation"`
	TurnCount        int     `json:"turnCount"`
	SpeakerCount     int     `json:"speakerCount"`
	ElapsedMS        int64   `json:"elapsedMs,omitempty"`
}

// SingleTrackDiarization holds the diarizer turns and the anonymous speaker
// numbering (spk_1, spk_2, ... in order of first spoken word, matching Cassini
// for Android) established over a single-track recording's primary transcript,
// so any secondary STT pass in the same build maps the same voice to the same
// speaker id.
type SingleTrackDiarization struct {
	turns   []SpeakerTurn
	ordinal map[int]int
	streams []AudioStream
}

// Streams returns the anonymous Speaker 1..N streams that received words.
func (d *SingleTrackDiarization) Streams() []AudioStream {
	if d == nil {
		return nil
	}
	return append([]AudioStream(nil), d.streams...)
}

// DiarizeSingleTrackSegments assigns the words of a single-track recording to
// anonymous top-level speakers (spk_1 "Speaker 1", spk_2 "Speaker 2", ...) by
// first spoken word. When fewer than two voices receive words, the transcript
// is returned unchanged with nil state so a one-person recording claims no
// separation.
func DiarizeSingleTrackSegments(segments []Segment, turns []SpeakerTurn) ([]Segment, *SingleTrackDiarization) {
	state := &SingleTrackDiarization{
		turns:   append([]SpeakerTurn(nil), turns...),
		ordinal: map[int]int{},
	}
	out := state.assign(segments)
	if len(state.streams) < 2 {
		return segments, nil
	}
	return out, state
}

// Apply assigns a secondary transcript's words to the same anonymous speakers
// measured on the recording, extending the roster if a voice that spoke no
// words in the primary pass receives words here.
func (d *SingleTrackDiarization) Apply(segments []Segment) []Segment {
	if d == nil {
		return segments
	}
	return d.assign(segments)
}

func (d *SingleTrackDiarization) assign(segments []Segment) []Segment {
	var words []Word
	var perSpeaker [][]Segment
	for _, seg := range segments {
		if len(seg.Words) > 0 {
			words = append(words, seg.Words...)
			continue
		}
		perSpeaker = append(perSpeaker, []Segment{seg})
	}
	if len(words) == 0 {
		return segments
	}
	sort.SliceStable(words, func(i, j int) bool { return words[i].StartMS < words[j].StartMS })
	assigned := assignWordsToTurns(words, d.turns)
	if assigned == nil {
		return segments
	}
	perVoice := map[int][]Word{}
	for i, rawSpeaker := range assigned {
		n, ok := d.ordinal[rawSpeaker]
		if !ok {
			n = len(d.ordinal) + 1
			d.ordinal[rawSpeaker] = n
			d.streams = append(d.streams, AudioStream{
				Index:        n - 1,
				SpeakerID:    fmt.Sprintf("spk_%d", n),
				SpeakerLabel: fmt.Sprintf("Speaker %d", n),
			})
		}
		perVoice[n] = append(perVoice[n], words[i])
	}
	for n := 1; n <= len(d.streams); n++ {
		if len(perVoice[n]) == 0 {
			continue
		}
		perSpeaker = append(perSpeaker, []Segment{{
			SpeakerID: fmt.Sprintf("spk_%d", n),
			Words:     perVoice[n],
		}})
	}
	return MergeAndSortSegments(perSpeaker)
}
