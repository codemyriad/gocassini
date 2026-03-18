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
		if got[index] != want[index] {
			t.Fatalf("word %d mismatch: got=%#v want=%#v", index, got[index], want[index])
		}
	}
}
