package transcribe

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func diarWord(start, end int64) Word { return Word{Text: "w", StartMS: start, EndMS: end} }

// voices maps the diarizer speakers to "spk_<n>", numbered by first
// assignment in word order — the shape the Android tests assert on.
func voices(words []Word, turns []SpeakerTurn) []string {
	assigned := assignWordsToTurns(words, turns)
	if assigned == nil {
		return nil
	}
	ordinal := map[int]int{}
	out := make([]string, len(assigned))
	for i, s := range assigned {
		if _, ok := ordinal[s]; !ok {
			ordinal[s] = len(ordinal) + 1
		}
		out[i] = fmt.Sprintf("spk_%d", ordinal[s])
	}
	return out
}

// The cases below port cassini-android DiarizationTest (feat/single-transcribe)
// so both producers assign words to voices identically.

func TestAssignWordsMajorityOverlapWinsAndIdsFollowFirstAppearance(t *testing.T) {
	turns := []SpeakerTurn{{0, 1000, 7}, {1000, 2000, 3}}
	got := voices([]Word{diarWord(100, 400), diarWord(900, 1300), diarWord(1100, 1500)}, turns)
	want := []string{"spk_1", "spk_2", "spk_2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAssignWordsInAGapTakesTheNearestTurn(t *testing.T) {
	turns := []SpeakerTurn{{0, 1000, 0}, {2000, 3000, 1}}
	got := voices([]Word{diarWord(1100, 1200), diarWord(1700, 1800)}, turns)
	if want := []string{"spk_1", "spk_2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAssignWordsWithoutValidTurnsAssignsNothing(t *testing.T) {
	words := []Word{diarWord(0, 10)}
	if got := assignWordsToTurns(words, nil); got != nil {
		t.Fatalf("no turns: got %v", got)
	}
	invalid := []SpeakerTurn{{-1, 10, 0}, {10, 0, 1}, {0, 0, 1}, {0, 20, -1}}
	if got := assignWordsToTurns(words, invalid); got != nil {
		t.Fatalf("invalid turns: got %v", got)
	}
}

func TestAssignWordsSumsDisjointTurnsPerSpeaker(t *testing.T) {
	turns := []SpeakerTurn{{0, 100, 0}, {100, 200, 1}, {200, 400, 0}}
	got := voices([]Word{diarWord(0, 50), diarWord(110, 150), diarWord(50, 300)}, turns)
	if want := []string{"spk_1", "spk_2", "spk_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAssignWordsOverlappingCopiesCannotDoubleCountTheSameSpeaker(t *testing.T) {
	turns := []SpeakerTurn{{0, 400, 0}, {200, 500, 0}, {1000, 1600, 1}, {1600, 1700, 1}}
	got := voices([]Word{diarWord(0, 10), diarWord(1010, 1020), diarWord(0, 1700)}, turns)
	if want := []string{"spk_1", "spk_2", "spk_2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAssignWordsTiesAreIndependentOfNativeOrder(t *testing.T) {
	turns := []SpeakerTurn{{0, 100, 8}, {200, 300, 1}}
	words := []Word{diarWord(0, 10), diarWord(200, 210), diarWord(50, 250), diarWord(150, 150)}
	got := voices(words, turns)
	reversed := slices.Clone(turns)
	slices.Reverse(reversed)
	if again := voices(words, reversed); !reflect.DeepEqual(got, again) {
		t.Fatalf("native order changed the result: %v vs %v", got, again)
	}
	if want := []string{"spk_1", "spk_2", "spk_1", "spk_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAssignWordsZeroLengthWordTakesNearestOverlappingTurn(t *testing.T) {
	turns := []SpeakerTurn{{0, 400, 0}, {900, 1400, 1}}
	got := voices([]Word{diarWord(1000, 1200), diarWord(0, 200), diarWord(1200, 1200)}, turns)
	if want := []string{"spk_1", "spk_2", "spk_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNormalizeTurnsDropsInvalidAndClamps(t *testing.T) {
	nan := 0.0
	nan = nan / nan
	raw := []rawTurn{
		{Start: 1.2, End: 2.5, Speaker: 1},
		{Start: -0.5, End: 0.4, Speaker: 0},
		{Start: 9.0, End: 12.0, Speaker: 0},
		{Start: nan, End: 1, Speaker: 0},
		{Start: 3, End: 3, Speaker: 0},
		{Start: 1, End: 2, Speaker: -1},
	}
	got := normalizeTurns(raw, 10000)
	want := []SpeakerTurn{{0, 400, 0}, {1200, 2500, 1}, {9000, 10000, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSplitSpeakerSegmentsSplitsOnlyTheSharedStream(t *testing.T) {
	gap := 2.5
	shared := "spk_room"
	segments := []Segment{
		{SpeakerID: shared, StartMS: 0, EndMS: 3000, Text: "a b c d", Words: []Word{
			{Text: "a", StartMS: 0, EndMS: 500},
			{Text: "b", StartMS: 600, EndMS: 1000, AttributionGapDB: gap, HasAttributionGap: true},
			{Text: "c", StartMS: 2000, EndMS: 2400, LowConfidenceSpeaker: true},
			{Text: "d", StartMS: 2500, EndMS: 3000},
		}},
		{SpeakerID: "spk_ben", StartMS: 1100, EndMS: 1900, Text: "x y", Words: []Word{
			{Text: "x", StartMS: 1100, EndMS: 1400},
			{Text: "y", StartMS: 1500, EndMS: 1900},
		}},
		{SpeakerID: shared, StartMS: 5000, EndMS: 5000, Text: "legacy text without words"},
	}
	// The diarizer calls the second voice 0 and the first 4: ids must follow
	// first spoken word, not the native labels.
	turns := []SpeakerTurn{{0, 1050, 4}, {1950, 3100, 0}}

	got, res := SplitSpeakerSegments(segments, shared, turns)

	wantIDs := []string{SubSpeakerID(shared, 1), SubSpeakerID(shared, 2)}
	if !reflect.DeepEqual(res.SubSpeakerIDs, wantIDs) {
		t.Fatalf("sub-speakers %v want %v", res.SubSpeakerIDs, wantIDs)
	}
	if !reflect.DeepEqual(res.WordsPerSpeaker, []int{2, 2}) || res.SpeakerCount != 2 || res.TurnCount != 2 {
		t.Fatalf("result %+v", res)
	}
	type turn struct {
		speaker string
		text    string
	}
	var turnsGot []turn
	for _, seg := range got {
		turnsGot = append(turnsGot, turn{seg.SpeakerID, seg.Text})
	}
	wantTurns := []turn{
		{wantIDs[0], "a b"},
		{"spk_ben", "x y"},
		{wantIDs[1], "c d"},
		{shared, "legacy text without words"},
	}
	if !reflect.DeepEqual(turnsGot, wantTurns) {
		t.Fatalf("segments %v want %v", turnsGot, wantTurns)
	}
	// Word timing and attribution evidence survive the split untouched.
	if w := got[0].Words[1]; !w.HasAttributionGap || w.AttributionGapDB != gap {
		t.Fatalf("attribution gap lost: %+v", w)
	}
	if w := got[2].Words[0]; !w.LowConfidenceSpeaker {
		t.Fatalf("low-confidence flag lost: %+v", w)
	}
}

func TestSplitSpeakerSegmentsIsDeterministic(t *testing.T) {
	segments := []Segment{{SpeakerID: "p", Words: []Word{diarWord(0, 100), diarWord(500, 600)}}}
	turns := []SpeakerTurn{{0, 200, 1}, {400, 700, 0}}
	a, ra := SplitSpeakerSegments(segments, "p", turns)
	b, rb := SplitSpeakerSegments(segments, "p", turns)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ra, rb) {
		t.Fatal("same audio and turns must give the same split")
	}
}

func TestSplitSpeakerSegmentsWithoutTurnsLeavesTranscriptAlone(t *testing.T) {
	segments := []Segment{{SpeakerID: "p", Text: "w", Words: []Word{diarWord(0, 100)}}}
	got, res := SplitSpeakerSegments(segments, "p", nil)
	if !reflect.DeepEqual(got, segments) || len(res.SubSpeakerIDs) != 0 {
		t.Fatalf("got %+v %+v", got, res)
	}
	got, res = SplitSpeakerSegments(segments, "absent", []SpeakerTurn{{0, 100, 0}})
	if !reflect.DeepEqual(got, segments) || len(res.SubSpeakerIDs) != 0 {
		t.Fatalf("absent speaker: got %+v %+v", got, res)
	}
}

func TestSplitSpeakerVoiceNumbersDependOnlyOnTheTurns(t *testing.T) {
	// Voice 1 speaks first in the audio but ASR found no words in its first
	// turn. Numbering by first word would call the later voice "1"; numbering
	// by first turn keeps the ids — and the names attached to them — stable
	// when the words are re-transcribed.
	turns := []SpeakerTurn{{0, 1000, 5}, {2000, 3000, 2}, {4000, 5000, 5}}
	first := []Segment{{SpeakerID: "p", Words: []Word{diarWord(2100, 2500), diarWord(4100, 4500)}}}
	_, a := SplitSpeakerSegments(first, "p", turns)
	if want := []string{"p~1", "p~2"}; !reflect.DeepEqual(a.SubSpeakerIDs, []string{"p~1", "p~2"}) {
		t.Fatalf("got %v want %v", a.SubSpeakerIDs, want)
	}
	rerun := []Segment{{SpeakerID: "p", Words: []Word{diarWord(2200, 2600)}}}
	got, b := SplitSpeakerSegments(rerun, "p", turns)
	if !reflect.DeepEqual(b.SubSpeakerIDs, []string{"p~2"}) || got[0].SpeakerID != "p~2" {
		t.Fatalf("voice without words must keep its number: %+v", b)
	}
}
