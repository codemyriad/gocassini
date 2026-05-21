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
