package operator

import (
	"strings"
	"testing"
)

// A row is the producer's own segment, passed through rather than re-cut. This
// is the property that makes a hit name the unit the viewer renders, and it is
// why the index does not need to know any segmentation rule.
func TestSearchRowsFromSegmentsPassTheProducersUnitsThrough(t *testing.T) {
	rows := searchRowsFromSegments([]searchReadableSegment{
		{ID: "seg_0002", SpeakerID: "S2", StartMS: 9_000, EndMS: 12_500, Text: "second"},
		{ID: "seg_0001", SpeakerID: "S1", StartMS: 1_000, EndMS: 4_000, Text: "first"},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	// Chronological, whatever order the artifact listed them in.
	if rows[0].SegmentID != "seg_0001" || rows[1].SegmentID != "seg_0002" {
		t.Fatalf("order = %s,%s, want seg_0001,seg_0002", rows[0].SegmentID, rows[1].SegmentID)
	}
	// Bounds and speaker are the producer's, untouched.
	if rows[0].StartMS != 1_000 || rows[0].EndMS != 4_000 || rows[0].SpeakerID != "S1" {
		t.Errorf("row = %+v, want the segment's own bounds and speaker", rows[0])
	}
	if rows[0].Text != "first" {
		t.Errorf("text = %q, want the segment's text verbatim", rows[0].Text)
	}
}

// The same artifact must produce the same rows every time, or "delete the index
// and rebuild it" cannot be checked against anything. Segments come from the
// .opus, so this is what makes a rebuild a rebuild rather than a re-derivation.
func TestSearchRowsFromSegmentsAreReproducible(t *testing.T) {
	segments := []searchReadableSegment{
		{ID: "c", SpeakerID: "S1", StartMS: 5_000, EndMS: 6_000, Text: "three"},
		{ID: "a", SpeakerID: "S2", StartMS: 1_000, EndMS: 2_000, Text: "one"},
		{ID: "b", SpeakerID: "S1", StartMS: 1_000, EndMS: 2_000, Text: "two"},
	}
	first := searchRowsFromSegments(segments)
	for i := 0; i < 20; i++ {
		again := searchRowsFromSegments(segments)
		if len(again) != len(first) {
			t.Fatalf("row count changed between runs: %d vs %d", len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("row %d changed between runs:\n first=%+v\n again=%+v", j, first[j], again[j])
			}
		}
	}
	// Rows sharing a start are still totally ordered, so ties cannot shuffle.
	// The documented order is (start, end, speaker, id), so the S1 row sorts
	// ahead of the S2 row even though its id is later in the alphabet.
	if first[0].SegmentID != "b" || first[1].SegmentID != "a" {
		t.Errorf("tie order = %s,%s, want b,a (speaker before id)", first[0].SegmentID, first[1].SegmentID)
	}
}

func TestSearchRowsFromSegmentsSkipUnusableEntries(t *testing.T) {
	rows := searchRowsFromSegments([]searchReadableSegment{
		{ID: "seg_0001", SpeakerID: "S1", StartMS: 1_000, EndMS: 2_000, Text: "   "},
		{ID: "seg_0002", SpeakerID: "S1", StartMS: 3_000, EndMS: 4_000, Text: "real"},
	})
	if len(rows) != 1 || rows[0].SegmentID != "seg_0002" {
		t.Fatalf("rows = %+v, want only the segment with text", rows)
	}
}

// An artifact whose segments carry no ids still indexes. Position is stable for
// an immutable artifact, so the synthetic id survives a rebuild.
func TestSearchRowsFromSegmentsNameUnidentifiedSegmentsStably(t *testing.T) {
	segments := []searchReadableSegment{
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 2_000, Text: "one"},
		{SpeakerID: "S1", StartMS: 3_000, EndMS: 4_000, Text: "two"},
	}
	first := searchRowsFromSegments(segments)
	again := searchRowsFromSegments(segments)
	if first[0].SegmentID != "seg_000000" || first[1].SegmentID != "seg_000001" {
		t.Fatalf("ids = %s,%s, want positional ids", first[0].SegmentID, first[1].SegmentID)
	}
	for i := range first {
		if first[i] != again[i] {
			t.Errorf("row %d is not reproducible: %+v vs %+v", i, first[i], again[i])
		}
	}
}

// Nonsense bounds are clamped rather than dropped: a reference to an instant is
// still a true reference, and losing the segment would lose searchable speech.
func TestSearchRowsFromSegmentsClampImpossibleBounds(t *testing.T) {
	rows := searchRowsFromSegments([]searchReadableSegment{
		{ID: "a", SpeakerID: "S1", StartMS: -50, EndMS: -10, Text: "before zero"},
		{ID: "b", SpeakerID: "S1", StartMS: 5_000, EndMS: 1_000, Text: "ends before it starts"},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both kept", rows)
	}
	for _, row := range rows {
		if row.StartMS < 0 || row.EndMS < row.StartMS {
			t.Errorf("row %s has impossible bounds [%d,%d]", row.SegmentID, row.StartMS, row.EndMS)
		}
	}
}

// --- the word-derived fallback, for meetings carrying no readable transcript

// Overlapping windows so a phrase cannot fall through a boundary. Only relevant
// to the fallback: a segment row holds a whole utterance already.
func TestDerivedRowsOverlapSoPhrasesSurviveBoundaries(t *testing.T) {
	rows := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 14_999, EndMS: 15_100, Text: "quarterly"},
		{SpeakerID: "S1", StartMS: 15_000, EndMS: 15_400, Text: "revenue"},
	})
	var together int
	for _, row := range rows {
		if strings.Contains(row.Text, "quarterly") && strings.Contains(row.Text, "revenue") {
			together++
		}
	}
	if together == 0 {
		t.Fatalf("no row holds both words, so the phrase cannot be found: %+v", rows)
	}
}

// A derived row cites the speech inside it, never the window's own bounds.
func TestDerivedRowsCiteSpeechNotWindowBounds(t *testing.T) {
	rows := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 20_000, EndMS: 20_400, Text: "acquisition"},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 overlapping windows", len(rows))
	}
	for _, row := range rows {
		if row.StartMS != 20_000 || row.EndMS != 20_400 {
			t.Errorf("row %s cited [%d,%d], want the speech span [20000,20400]",
				row.SegmentID, row.StartMS, row.EndMS)
		}
	}
}

// Membership depends only on a word's own timestamp, so inserting a word
// perturbs its own windows and nothing else.
func TestDerivedRowsPerturbLocallyWhenAWordIsInserted(t *testing.T) {
	find := func(rows []searchRow, id string) (searchRow, bool) {
		for _, row := range rows {
			if row.SegmentID == id {
				return row, true
			}
		}
		return searchRow{}, false
	}
	before := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_200, Text: "alpha"},
		{SpeakerID: "S1", StartMS: 200_000, EndMS: 200_400, Text: "omega"},
	})
	after := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_200, Text: "alpha"},
		{SpeakerID: "S1", StartMS: 1_500, EndMS: 1_700, Text: "inserted"},
		{SpeakerID: "S1", StartMS: 200_000, EndMS: 200_400, Text: "omega"},
	})
	beforeFar, ok := find(before, "w_000195000_S1")
	if !ok {
		t.Fatalf("expected a far window: %+v", before)
	}
	afterFar, ok := find(after, "w_000195000_S1")
	if !ok {
		t.Fatalf("expected a far window: %+v", after)
	}
	if beforeFar != afterFar {
		t.Errorf("a distant row moved when a word was inserted:\n before=%+v\n after=%+v", beforeFar, afterFar)
	}
}

func TestDerivedRowsSplitBySpeaker(t *testing.T) {
	rows := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_200, Text: "yes"},
		{SpeakerID: "S2", StartMS: 1_100, EndMS: 1_300, Text: "no"},
	})
	for _, row := range rows {
		if strings.Contains(row.Text, "yes") && strings.Contains(row.Text, "no") {
			t.Fatalf("two speakers share a row: %+v", row)
		}
	}
}

func TestDerivedRowsSkipUnplaceableWords(t *testing.T) {
	rows := deriveSearchRowsFromWords([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_100, Text: "   "},
		{SpeakerID: "S1", StartMS: -5, EndMS: 10, Text: "before the meeting"},
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_100, Text: "real"},
	})
	if len(rows) != 1 || rows[0].Text != "real" {
		t.Fatalf("rows = %+v, want only the placeable word", rows)
	}
}

func TestDerivedRowsAreReproducible(t *testing.T) {
	words := []searchTranscriptWord{
		{SpeakerID: "S2", StartMS: 40_000, EndMS: 40_300, Text: "later"},
		{SpeakerID: "S1", StartMS: 1_000, EndMS: 1_200, Text: "first"},
		{SpeakerID: "S3", StartMS: 40_000, EndMS: 40_300, Text: "also"},
	}
	first := deriveSearchRowsFromWords(words)
	for i := 0; i < 20; i++ {
		again := deriveSearchRowsFromWords(words)
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("row %d changed between runs:\n first=%+v\n again=%+v", j, first[j], again[j])
			}
		}
	}
}
