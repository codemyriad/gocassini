package transcribe

import "testing"

func TestTokensToWordsSplitsPhraseSizedTokens(t *testing.T) {
	got := tokensToWords(
		[]string{"Test and I needed", "▁to", "▁run"},
		[]float32{0, 4, 5},
		[]float32{4, 1, 1},
	)

	want := []Word{
		{Text: "Test", StartMS: 0, EndMS: 1000},
		{Text: "and", StartMS: 1000, EndMS: 2000},
		{Text: "I", StartMS: 2000, EndMS: 3000},
		{Text: "needed", StartMS: 3000, EndMS: 4000},
		{Text: "to", StartMS: 4000, EndMS: 5000},
		{Text: "run", StartMS: 5000, EndMS: 6000},
	}

	if len(got) != len(want) {
		t.Fatalf("word count mismatch: got=%d want=%d (%#v)", len(got), len(want), got)
	}
	for index := range want {
		if !sameWordWithinMillisecond(got[index], want[index]) {
			t.Fatalf("word %d mismatch: got=%#v want=%#v", index, got[index], want[index])
		}
	}
}

func TestTokensToWordsRespectsLeadingSpaceWordStarts(t *testing.T) {
	got := tokensToWords(
		[]string{"Sp", "irit", ".", " So", " we'll", " What"},
		[]float32{0.24, 0.40, 0.72, 0.88, 1.04, 7.76},
		[]float32{0.16, 0.16, 0.16, 0.16, 0.32, 0.16},
	)

	// The leading-space forms (" So", " we'll", " What") must each open a new
	// word at their own timestamp; collapsing them into one token is what this
	// case was written to catch. "Spirit." ends where its last spoken token
	// ends (400+160ms), not where the "." token is stamped: Parakeet places a
	// sentence-final mark at the onset of the next utterance, so honouring it
	// would stretch the word 320ms into the following pause.
	want := []Word{
		{Text: "Spirit.", StartMS: 240, EndMS: 560},
		{Text: "So", StartMS: 880, EndMS: 1040},
		{Text: "we'll", StartMS: 1040, EndMS: 1360},
		{Text: "What", StartMS: 7760, EndMS: 7920},
	}

	if len(got) != len(want) {
		t.Fatalf("word count mismatch: got=%d want=%d (%#v)", len(got), len(want), got)
	}
	for index := range want {
		if !sameWordWithinMillisecond(got[index], want[index]) {
			t.Fatalf("word %d mismatch: got=%#v want=%#v", index, got[index], want[index])
		}
	}
}

func TestTokensToWordsKeepsZeroDurationWordStartsNonNegative(t *testing.T) {
	got := tokensToWords(
		[]string{"decompression", " so", " i"},
		[]float32{1325.205, 1325.925, 1326.005},
		[]float32{0.56, 0, 0.08},
	)

	want := []Word{
		{Text: "decompression", StartMS: 1325205, EndMS: 1325765},
		{Text: "so", StartMS: 1325925, EndMS: 1325925},
		{Text: "i", StartMS: 1326005, EndMS: 1326085},
	}

	if len(got) != len(want) {
		t.Fatalf("word count mismatch: got=%d want=%d (%#v)", len(got), len(want), got)
	}
	for index := range want {
		if !sameWordWithinMillisecond(got[index], want[index]) {
			t.Fatalf("word %d mismatch: got=%#v want=%#v", index, got[index], want[index])
		}
	}
}

func TestTokensToWordsKeepsPunctuationOutOfWordEnds(t *testing.T) {
	// Punctuation tokens are stamped at the next acoustic onset. None of the
	// forms below may push a word's end past its last spoken token: a single
	// mark, several marks in a row, a mark inside a word, and a mark following
	// a phrase-sized token that splitMultiWordTokens then divides. Every mark
	// stays in the word's text.
	got := tokensToWords(
		[]string{"▁Right", "?", "!", "▁U", ".", "S", ".", "▁okay then", ".", "▁Next"},
		[]float32{1.00, 3.50, 3.52, 3.60, 3.72, 3.80, 3.96, 4.20, 9.00, 9.10},
		[]float32{0.40, 0.08, 0.08, 0.12, 0.08, 0.16, 0.08, 0.80, 0.08, 0.20},
	)

	want := []Word{
		{Text: "Right?!", StartMS: 1000, EndMS: 1400},
		{Text: "U.S.", StartMS: 3600, EndMS: 3960},
		{Text: "okay", StartMS: 4200, EndMS: 4600},
		{Text: "then.", StartMS: 4600, EndMS: 5000},
		{Text: "Next", StartMS: 9100, EndMS: 9300},
	}

	assertWords(t, got, want)
}

func TestTokensToWordsKeepsSpokenSymbolsExtendingTheEnd(t *testing.T) {
	// Only delimiter marks that are never spoken are excluded. A symbol that
	// stands for a spoken word ("percent") carries real audio and must still
	// decide where the word ends; the sentence-final mark after it must not.
	got := tokensToWords(
		[]string{"▁50", "%", ".", "▁Next"},
		[]float32{1.00, 1.40, 2.60, 2.70},
		[]float32{0.30, 0.50, 0.08, 0.20},
	)

	want := []Word{
		{Text: "50%.", StartMS: 1000, EndMS: 1900},
		{Text: "Next", StartMS: 2700, EndMS: 2900},
	}

	assertWords(t, got, want)
}

func TestTokensToWordsKeepsDrawnOutWordsWhole(t *testing.T) {
	// A genuinely long word is continuous speech across several tokens. The
	// punctuation rule must not shorten it, and the trailing mark must not
	// lengthen it either.
	got := tokensToWords(
		[]string{"▁sooo", "ooo", "ooo", "!", "▁Done"},
		[]float32{2.00, 2.60, 3.20, 3.80, 5.00},
		[]float32{0.60, 0.60, 0.60, 0.08, 0.30},
	)

	want := []Word{
		{Text: "sooooooooo!", StartMS: 2000, EndMS: 3800},
		{Text: "Done", StartMS: 5000, EndMS: 5300},
	}

	assertWords(t, got, want)
}

func TestTokensToWordsGivesPunctuationOnlyWordsNoExtent(t *testing.T) {
	// A word whose every token is punctuation has no acoustic evidence at all.
	// Keep it — dropping words would desynchronise the cleaned-text alignment —
	// but give it no extent rather than the span of the pause it sits in.
	got := tokensToWords(
		[]string{"▁Wait", "▁...", "▁Then"},
		[]float32{1.00, 4.00, 4.10},
		[]float32{0.30, 0.50, 0.25},
	)

	want := []Word{
		{Text: "Wait", StartMS: 1000, EndMS: 1300},
		{Text: "...", StartMS: 4000, EndMS: 4000},
		{Text: "Then", StartMS: 4100, EndMS: 4350},
	}

	assertWords(t, got, want)
	for _, word := range got {
		if word.EndMS < word.StartMS {
			t.Fatalf("word %#v ends before it starts", word)
		}
	}
}

func TestTokensToWordsDoesNotInheritThePreviousWordEnd(t *testing.T) {
	// The word end is per word. A zero-duration token stamped before the
	// previous word's end used to inherit that end through the running maximum,
	// which backdated the word's start into the previous speaker's audio.
	got := tokensToWords(
		[]string{"▁decompression", "▁mm"},
		[]float32{1.00, 1.20},
		[]float32{0.56, 0},
	)

	want := []Word{
		{Text: "decompression", StartMS: 1000, EndMS: 1560},
		{Text: "mm", StartMS: 1200, EndMS: 1200},
	}

	assertWords(t, got, want)
}

func assertWords(t *testing.T, got, want []Word) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("word count mismatch: got=%d want=%d (%#v)", len(got), len(want), got)
	}
	for index := range want {
		if !sameWordWithinMillisecond(got[index], want[index]) {
			t.Fatalf("word %d mismatch: got=%#v want=%#v", index, got[index], want[index])
		}
	}
}

func sameWordWithinMillisecond(got, want Word) bool {
	return got.Text == want.Text && absInt64(got.StartMS-want.StartMS) <= 1 && absInt64(got.EndMS-want.EndMS) <= 1
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// The punctuation-inclusive end is not thrown away: it travels with the word as
// a ceiling the energy gate may extend up to, and no further. The end itself
// still stops at the last speech-bearing token. A phrase-sized token that
// splitMultiWordTokens divides passes the ceiling to its final part only, since
// the interior parts end where their neighbours begin.
func TestTokensToWordsCarriesThePunctuationEndAsACapOnly(t *testing.T) {
	got := tokensToWords(
		[]string{"▁Right", "?", "!", "▁U", ".", "S", ".", "▁okay then", ".", "▁Next"},
		[]float32{1.00, 3.50, 3.52, 3.60, 3.72, 3.80, 3.96, 4.20, 9.00, 9.10},
		[]float32{0.40, 0.08, 0.08, 0.12, 0.08, 0.16, 0.08, 0.80, 0.08, 0.20},
	)

	type capCase struct {
		text   string
		endMS  int64
		capMS  int64
		hasCap bool
	}
	want := []capCase{
		{text: "Right?!", endMS: 1400, capMS: 3600, hasCap: true},
		{text: "U.S.", endMS: 3960, capMS: 4040, hasCap: true},
		{text: "okay", endMS: 4600, capMS: 4600},
		{text: "then.", endMS: 5000, capMS: 9080, hasCap: true},
		{text: "Next", endMS: 9300, capMS: 9300},
	}
	if len(got) != len(want) {
		t.Fatalf("word count mismatch: got=%d want=%d (%#v)", len(got), len(want), got)
	}
	for index, expect := range want {
		word := got[index]
		if word.Text != expect.text || absInt64(word.EndMS-expect.endMS) > 1 {
			t.Fatalf("word %d = %q %d-%dms; want %q ending at %dms", index, word.Text, word.StartMS, word.EndMS, expect.text, expect.endMS)
		}
		// extentCapMS is what the energy gate reads: an unset cap collapses to
		// the word's own end, which permits no extension at all.
		if absInt64(word.extentCapMS()-expect.capMS) > 1 {
			t.Errorf("word %q cap = %dms; want %dms", word.Text, word.extentCapMS(), expect.capMS)
		}
		if expect.hasCap && word.extentCapMS() <= word.EndMS {
			t.Errorf("word %q must carry a ceiling above its end, got %d <= %d", word.Text, word.extentCapMS(), word.EndMS)
		}
		if !expect.hasCap && word.extentCap > word.EndMS {
			t.Errorf("word %q must not carry a ceiling, got %d", word.Text, word.extentCap)
		}
	}
}

// The ceiling is per word for the same reason the end is: a word must never
// inherit a neighbour's reach.
func TestTokensToWordsDoesNotInheritThePreviousCap(t *testing.T) {
	// The zero-duration boundary token is stamped a long way before the mark
	// that closed the previous word, so a leaked ceiling is unmistakable.
	got := tokensToWords(
		[]string{"▁Done", ".", "▁mm"},
		[]float32{1.00, 9.00, 1.20},
		[]float32{0.56, 0.08, 0},
	)

	if len(got) != 2 {
		t.Fatalf("got %#v; want two words", got)
	}
	if absInt64(got[0].extentCapMS()-9080) > 1 {
		t.Fatalf("first word ceiling = %dms; want the mark at 9080ms", got[0].extentCapMS())
	}
	if got[1].extentCapMS() != got[1].EndMS {
		t.Fatalf("second word inherited a ceiling of %dms; want its own end %dms", got[1].extentCapMS(), got[1].EndMS)
	}
}
