package operator

import (
	"strings"
	"testing"
)

// A word belongs to the window its own startMs falls in, and to the previous
// one — windows are twice the stride wide, so they overlap.
func TestBuildSearchWindowsOverlapSoPhrasesSurviveBoundaries(t *testing.T) {
	// 14999 and 15000 straddle a stride boundary. Disjoint buckets would put
	// them in different rows and lose the phrase; overlapping ones must not.
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 14_999, Text: "quarterly"},
		{SpeakerID: "S1", StartMS: 15_000, Text: "revenue"},
	})
	var together int
	for _, window := range windows {
		if strings.Contains(window.Text, "quarterly") && strings.Contains(window.Text, "revenue") {
			together++
		}
	}
	if together == 0 {
		t.Fatalf("no window holds both words, so the phrase cannot be found: %+v", windows)
	}
}

// Membership is a pure function of a word's own startMs, so inserting a word
// perturbs only the windows that word falls in — every other window's start_ms
// is byte-identical. That is what a segment-keyed index cannot promise, and it
// is what makes a saved reference survive a re-transcription.
func TestBuildSearchWindowsPerturbLocallyWhenAWordIsInserted(t *testing.T) {
	before := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, Text: "alpha"},
		{SpeakerID: "S1", StartMS: 200_000, Text: "omega"},
	})
	after := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, Text: "alpha"},
		{SpeakerID: "S1", StartMS: 1_500, Text: "inserted"},
		{SpeakerID: "S1", StartMS: 200_000, Text: "omega"},
	})

	find := func(windows []searchWindow, startMS int64) (searchWindow, bool) {
		for _, window := range windows {
			if window.BucketMS == startMS {
				return window, true
			}
		}
		return searchWindow{}, false
	}
	// The far window must be untouched, bounds and text alike.
	for _, windows := range [][]searchWindow{before, after} {
		if _, ok := find(windows, 195_000); !ok {
			t.Fatalf("expected a window at 195000: %+v", windows)
		}
	}
	beforeFar, _ := find(before, 195_000)
	afterFar, _ := find(after, 195_000)
	if beforeFar != afterFar {
		t.Errorf("a distant window moved when a word was inserted:\n before=%+v\n after=%+v", beforeFar, afterFar)
	}
}

// Rows are split by speaker so a per-speaker filter is free and every reference
// is attributable.
func TestBuildSearchWindowsSplitBySpeaker(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, Text: "yes"},
		{SpeakerID: "S2", StartMS: 1_100, Text: "no"},
	})
	for _, window := range windows {
		if strings.Contains(window.Text, "yes") && strings.Contains(window.Text, "no") {
			t.Fatalf("two speakers share a row: %+v", window)
		}
	}
	speakers := map[string]bool{}
	for _, window := range windows {
		speakers[window.SpeakerID] = true
	}
	if !speakers["S1"] || !speakers["S2"] {
		t.Errorf("speakers = %v, want both S1 and S2", speakers)
	}
}

// Deterministic output: the same words must produce byte-identical rows, or
// "delete the index and rebuild" cannot be checked against anything.
func TestBuildSearchWindowsAreDeterministic(t *testing.T) {
	words := []searchTranscriptWord{
		{SpeakerID: "S2", StartMS: 40_000, Text: "later"},
		{SpeakerID: "S1", StartMS: 1_000, Text: "first"},
		{SpeakerID: "S3", StartMS: 40_000, Text: "also"},
	}
	first := buildSearchWindows(words)
	for i := 0; i < 20; i++ {
		again := buildSearchWindows(words)
		if len(again) != len(first) {
			t.Fatalf("row count changed between runs: %d vs %d", len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("row %d changed between runs:\n first=%+v\n again=%+v", j, first[j], again[j])
			}
		}
	}
	// Ordered by (start, speaker), never by map iteration.
	for i := 1; i < len(first); i++ {
		if first[i-1].BucketMS > first[i].BucketMS {
			t.Fatalf("rows are not ordered by start: %+v", first)
		}
	}
}

func TestBuildSearchWindowsSkipsUnplaceableWords(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 1_000, Text: "   "},
		{SpeakerID: "S1", StartMS: -5, Text: "before the meeting"},
		{SpeakerID: "S1", StartMS: 1_000, Text: "real"},
	})
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want exactly one", windows)
	}
	if windows[0].Text != "real" {
		t.Errorf("text = %q, want only the placeable word", windows[0].Text)
	}
}

// A word at the very start has no previous window to belong to.
func TestBuildSearchWindowsAtTheStartOfAMeeting(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 0, Text: "hello"},
	})
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want exactly one", windows)
	}
	if windows[0].BucketMS != 0 {
		t.Errorf("bucket = %d, want 0", windows[0].BucketMS)
	}
}

// A word carrying no speaker is still a true reference — somebody said this,
// here — so it is kept rather than dropped.
func TestBuildSearchWindowsKeepsSpeakerlessWords(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{StartMS: 1_000, Text: "unattributed"},
	})
	if len(windows) != 1 || windows[0].SpeakerID != "" {
		t.Fatalf("windows = %+v, want one row under the empty speaker id", windows)
	}
}

// A row's reference must be the speech inside it, never the bucket's own
// bounds — otherwise every hit sends the reader to a 30-second haystack that
// starts wherever the arithmetic landed rather than where anyone spoke.
func TestBuildSearchWindowsCiteSpeechNotBucketBounds(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 20_000, EndMS: 20_400, Text: "acquisition"},
	})
	for _, window := range windows {
		if window.StartMS != 20_000 || window.EndMS != 20_400 {
			t.Errorf("bucket %d cited [%d,%d], want the speech span [20000,20400]",
				window.BucketMS, window.StartMS, window.EndMS)
		}
		if window.StartMS == window.BucketMS && window.BucketMS != 20_000 {
			t.Errorf("bucket %d cited its own start instead of the speech", window.BucketMS)
		}
	}
	// The same word lands in two overlapping buckets, and both cite the speech.
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want 2 overlapping buckets", len(windows))
	}
	if windows[0].BucketMS != 0 || windows[1].BucketMS != 15_000 {
		t.Errorf("buckets = %d,%d, want 0 and 15000", windows[0].BucketMS, windows[1].BucketMS)
	}
}

// A word with no usable end degrades to a point in time rather than being
// dropped: a reference to an instant is still a true reference.
func TestBuildSearchWindowsToleratesAMissingWordEnd(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 5_000, Text: "unbounded"},
	})
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want one", windows)
	}
	if windows[0].StartMS != 5_000 || windows[0].EndMS != 5_000 {
		t.Errorf("span = [%d,%d], want the instant [5000,5000]", windows[0].StartMS, windows[0].EndMS)
	}
}

// Words are not assumed to arrive in order: the span widens to cover them.
func TestBuildSearchWindowsWidenTheSpanRegardlessOfWordOrder(t *testing.T) {
	windows := buildSearchWindows([]searchTranscriptWord{
		{SpeakerID: "S1", StartMS: 9_000, EndMS: 9_500, Text: "later"},
		{SpeakerID: "S1", StartMS: 2_000, EndMS: 2_300, Text: "earlier"},
	})
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want one", windows)
	}
	if windows[0].StartMS != 2_000 || windows[0].EndMS != 9_500 {
		t.Errorf("span = [%d,%d], want [2000,9500]", windows[0].StartMS, windows[0].EndMS)
	}
}
