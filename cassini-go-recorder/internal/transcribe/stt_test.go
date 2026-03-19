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

	want := []Word{
		{Text: "Spirit.", StartMS: 240, EndMS: 880},
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

func sameWordWithinMillisecond(got, want Word) bool {
	return got.Text == want.Text && absInt64(got.StartMS-want.StartMS) <= 1 && absInt64(got.EndMS-want.EndMS) <= 1
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
