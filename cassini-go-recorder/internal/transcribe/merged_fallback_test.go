package transcribe

// These tests pin the merged-fallback trigger: the strict ==0 gate
// previously let a single stray word from one struggling per-participant
// track suppress the merged-mix fallback, which is the recovery path
// for exactly the runs where per-participant transcription is failing.
// CI flake-hunt run 26210020184 matrix #5 produced 1 word total and
// skipped fallback, taking the e2e suite red.

import "testing"

func segmentsWithWords(n int) []Segment {
	if n <= 0 {
		return nil
	}
	words := make([]Word, n)
	for i := range words {
		words[i] = Word{Text: "x", StartMS: int64(i * 100), EndMS: int64(i*100 + 50)}
	}
	return []Segment{{
		SpeakerID: "s1",
		Words:     words,
	}}
}

func TestShouldFireMergedFallbackOnZeroWords(t *testing.T) {
	if !shouldFireMergedFallback(nil) {
		t.Fatalf("shouldFireMergedFallback(nil) = false; want true (the original failure mode)")
	}
	if !shouldFireMergedFallback([]Segment{}) {
		t.Fatalf("shouldFireMergedFallback([]) = false; want true")
	}
}

func TestShouldFireMergedFallbackOnSingleStrayWord(t *testing.T) {
	// The exact regression: CI flake-hunt matrix #5 produced 1 word total
	// from per-participant transcription. The old `> 0` gate let that
	// stray word suppress the merged-mix recovery pass.
	if !shouldFireMergedFallback(segmentsWithWords(1)) {
		t.Fatalf("shouldFireMergedFallback(1 word) = false; want true (this is the bug — a single stray word must NOT suppress merged-mix recovery)")
	}
}

func TestShouldFireMergedFallbackJustBelowThreshold(t *testing.T) {
	if !shouldFireMergedFallback(segmentsWithWords(minWordsBeforeMergedFallback - 1)) {
		t.Fatalf("shouldFireMergedFallback(%d words) = false; want true", minWordsBeforeMergedFallback-1)
	}
}

func TestShouldFireMergedFallbackAtThreshold(t *testing.T) {
	if shouldFireMergedFallback(segmentsWithWords(minWordsBeforeMergedFallback)) {
		t.Fatalf("shouldFireMergedFallback(%d words) = true; want false (threshold is inclusive — exactly N words is enough confidence to skip fallback)", minWordsBeforeMergedFallback)
	}
}

func TestShouldFireMergedFallbackOnHealthyPass(t *testing.T) {
	// A healthy per-participant pass produces ~12-14 verbatim words.
	// Fallback must skip in that case to avoid wasting a second
	// recognizer + extra pass per recording.
	if shouldFireMergedFallback(segmentsWithWords(14)) {
		t.Fatalf("shouldFireMergedFallback(14 words) = true; want false (healthy passes must not trigger redundant recovery)")
	}
}

func TestChooseMergedFallbackReplacesThinPassWithoutDuplicatingSurvivors(t *testing.T) {
	participant := []Segment{{
		SpeakerID: "spk_alice",
		StartMS:   1000,
		EndMS:     1500,
		Words: []Word{
			{Text: "hello", StartMS: 1000, EndMS: 1200},
			{Text: "there", StartMS: 1250, EndMS: 1500},
		},
	}}
	merged := []Segment{{
		SpeakerID: "merged",
		StartMS:   900,
		EndMS:     2200,
		Words: []Word{
			{Text: "well", StartMS: 900, EndMS: 980},
			{Text: "hello", StartMS: 1000, EndMS: 1200},
			{Text: "there", StartMS: 1250, EndMS: 1500},
			{Text: "everyone", StartMS: 1700, EndMS: 2200},
		},
	}}

	got, useMerged := chooseMergedFallback(participant, merged)
	if !useMerged {
		t.Fatal("richer mixed pass was not selected")
	}
	if CountWords(got) != CountWords(merged) {
		t.Fatalf("selected transcript has %d words, want exactly the mixed pass's %d (no additive duplicates)", CountWords(got), CountWords(merged))
	}
	for _, segment := range got {
		if segment.SpeakerID != "merged" {
			t.Fatalf("thin participant hypothesis leaked into selected transcript: %#v", got)
		}
	}
}

func TestChooseMergedFallbackKeepsAttributionUnlessMixIsStrictlyRicher(t *testing.T) {
	participant := segmentsWithWords(3)
	participant[0].SpeakerID = "spk_alice"

	for _, test := range []struct {
		name            string
		mergedWordCount int
	}{
		{name: "empty", mergedWordCount: 0},
		{name: "poorer", mergedWordCount: 2},
		{name: "tied", mergedWordCount: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			merged := segmentsWithWords(test.mergedWordCount)
			if len(merged) != 0 {
				merged[0].SpeakerID = "merged"
			}
			got, useMerged := chooseMergedFallback(participant, merged)
			if useMerged {
				t.Fatalf("selected %d-word mixed pass over 3 attributed words", test.mergedWordCount)
			}
			if len(got) != 1 || got[0].SpeakerID != "spk_alice" || CountWords(got) != 3 {
				t.Fatalf("attributed participant pass was not preserved: %#v", got)
			}
		})
	}
}
