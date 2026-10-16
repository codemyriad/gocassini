package transcribe

import (
	"strings"
	"testing"
)

// Parakeet Ultra emits a literal <unk> on greedy paths. It must never reach a
// word: not glued onto its neighbour, not as a word of its own, and the tokens
// around it must keep their own timings.
func TestTokensToWordsDropsUnknownTokens(t *testing.T) {
	got := tokensToWords(
		[]string{"▁hel", "<unk>", "lo", "▁<unk>", "▁wor", "ld", "<unk>", "."},
		[]float32{0.00, 0.20, 0.40, 0.80, 1.20, 1.40, 1.60, 1.80},
		[]float32{0.20, 0.20, 0.20, 0.20, 0.20, 0.20, 0.80, 0.00},
	)
	want := []Word{
		{Text: "hello", StartMS: 0, EndMS: 600},
		{Text: "world.", StartMS: 1200, EndMS: 1600},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d words, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if strings.Contains(got[i].Text, unknownToken) {
			t.Fatalf("word %d still carries %s: %#v", i, unknownToken, got[i])
		}
		if !sameWordWithinMillisecond(got[i], want[i]) {
			t.Fatalf("word %d: got %#v, want %#v", i, got[i], want[i])
		}
	}
}

// A decode that is nothing but <unk> yields no words at all.
func TestTokensToWordsOnlyUnknownYieldsNothing(t *testing.T) {
	if got := tokensToWords([]string{"<unk>", "▁<unk>"}, []float32{0, 0.5}, []float32{0.5, 0.5}); len(got) != 0 {
		t.Fatalf("got %#v, want no words", got)
	}
}

func TestWithoutUnknownTokensKeepsSlicesAligned(t *testing.T) {
	tokens, ts, dur := withoutUnknownTokens(
		[]string{"▁a", "<unk>", "▁<unk>", "b"},
		[]float32{1, 2, 3, 4},
		[]float32{10, 20, 30, 40},
	)
	if strings.Join(tokens, "|") != "▁a|▁|b" {
		t.Fatalf("tokens = %q", tokens)
	}
	if len(ts) != 3 || ts[0] != 1 || ts[1] != 3 || ts[2] != 4 {
		t.Fatalf("timestamps = %v", ts)
	}
	if len(dur) != 3 || dur[0] != 10 || dur[1] != 30 || dur[2] != 40 {
		t.Fatalf("durations = %v", dur)
	}
}
