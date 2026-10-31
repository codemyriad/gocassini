package topics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeCounter counts a fixed number of tokens for the prompt and the BOS and
// EOS, and perWord for each space-separated word, or perRune for each rune
// when set. Real ids do not matter to the chunker, only their number.
type fakeCounter struct{ perWord, perRune int }

func (f fakeCounter) Encode(prefix, text string) []int64 {
	n := 9
	if f.perRune > 0 {
		n += f.perRune * utf8.RuneCountInString(text)
	} else {
		n += f.perWord * len(strings.Fields(text))
	}
	return make([]int64, n)
}

type chunkCase struct {
	Name     string             `json:"name"`
	Words    [][4]any           `json:"words"`
	Passages []chunkCasePassage `json:"passages"`
}

type chunkCasePassage struct {
	StartMS   int64  `json:"startMs"`
	EndMS     int64  `json:"endMs"`
	Text      string `json:"text"`
	WordCount int    `json:"wordCount"`
	Boundary  string `json:"boundary"`
}

func loadChunkCases(t *testing.T) []chunkCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/chunk_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []chunkCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func (c chunkCase) words() []Word {
	out := make([]Word, len(c.Words))
	for i, w := range c.Words {
		out[i] = Word{Text: w[0].(string), StartMS: int64(w[1].(float64)), EndMS: int64(w[2].(float64)), Speaker: w[3].(string)}
	}
	return out
}

func comparable(passages []Passage) []chunkCasePassage {
	out := make([]chunkCasePassage, len(passages))
	for i, p := range passages {
		out[i] = chunkCasePassage{StartMS: p.StartMS, EndMS: p.EndMS, Text: p.Text, WordCount: p.WordCount, Boundary: p.Boundary}
	}
	return out
}

// The Go chunker gives exactly the prototype's passages
// (testdata/reference/chunks.py) on synthetic transcripts: pauses, speaker
// changes, sentence ends with quotes, empty and padded words, overlapping
// words and equal times.
func TestChunkMatchesThePrototype(t *testing.T) {
	assertChunkCases(t, fakeCounter{perWord: 2})
}

// The same with the real tokenizer, which the token cap consults.
func TestChunkMatchesThePrototypeWithTheTokenizer(t *testing.T) {
	tok, err := LoadTokenizer(modelFiles(t).Tokenizer)
	if err != nil {
		t.Fatal(err)
	}
	assertChunkCases(t, tok)
}

func assertChunkCases(t *testing.T, tok TokenCounter) {
	t.Helper()
	cases := loadChunkCases(t)
	if len(cases) < 10 {
		t.Fatalf("only %d chunk cases", len(cases))
	}
	for _, c := range cases {
		got := comparable(Chunk(c.words(), tok))
		if len(got) == 0 && len(c.Passages) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Passages) {
			for i := range min(len(got), len(c.Passages)) {
				if got[i] != c.Passages[i] {
					t.Errorf("%s: passage %d = %+v, want %+v", c.Name, i, got[i], c.Passages[i])
					break
				}
			}
			t.Errorf("%s: %d passages, want %d", c.Name, len(got), len(c.Passages))
		}
	}
}

func TestChunkKeepsEveryWordOnce(t *testing.T) {
	for _, c := range loadChunkCases(t) {
		words := c.words()
		passages := Chunk(words, fakeCounter{perWord: 2})
		var want []string
		for _, p := range comparable(Chunk(words, fakeCounter{perWord: 2})) {
			want = append(want, p.Text)
		}
		var got, kept []string
		total := 0
		for i, p := range passages {
			if p.Ordinal != i {
				t.Fatalf("%s: passage %d has ordinal %d", c.Name, i, p.Ordinal)
			}
			got = append(got, p.Text)
			total += p.WordCount
		}
		// Deterministic: the same words give the same passages.
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: two runs differ", c.Name)
		}
		for _, w := range words {
			if s := strings.TrimSpace(w.Text); s != "" {
				kept = append(kept, s)
			}
		}
		if total != len(kept) {
			t.Fatalf("%s: passages hold %d words, the transcript %d", c.Name, total, len(kept))
		}
		// Sorted words, joined: every word once. The fixtures' only reorder is
		// overlap, so compare as multisets.
		if a, b := strings.Fields(strings.Join(got, " ")), kept; !sameMultiset(a, b) {
			t.Fatalf("%s: passage words differ from the transcript's", c.Name)
		}
	}
}

func sameMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}

func speech(n int, text string) []Word {
	words := make([]Word, n)
	for i := range words {
		words[i] = Word{Text: text, StartMS: int64(i) * 100, EndMS: int64(i)*100 + 90, Speaker: "a"}
	}
	return words
}

// A run of speech with no pause, turn or sentence end, whose words cost many
// tokens, is split at the word before it would pass MaxTokens, at word
// boundaries and never truncated.
func TestChunkSplitsAtTheTokenCap(t *testing.T) {
	tok := fakeCounter{perWord: 20} // (512-9)/20: 25 words fit
	words := speech(70, "Donaudampfschifffahrtsgesellschaft")
	passages := Chunk(words, tok)
	var counts []int
	for _, p := range passages {
		counts = append(counts, p.WordCount)
		if p.TokenCount > MaxTokens {
			t.Fatalf("passage %d has %d tokens", p.Ordinal, p.TokenCount)
		}
		if p.TokenCount != len(tok.Encode(PassagePrefix, p.Text)) {
			t.Fatalf("passage %d reports %d tokens, its text has %d", p.Ordinal, p.TokenCount, len(tok.Encode(PassagePrefix, p.Text)))
		}
	}
	if fmt.Sprint(counts) != "[25 25 20]" || passages[0].Boundary != boundaryTokenCap || passages[2].Boundary != boundaryEnd {
		t.Fatalf("passages %v, boundaries %q %q", counts, passages[0].Boundary, passages[2].Boundary)
	}
	if got := strings.Fields(passages[0].Text + " " + passages[1].Text + " " + passages[2].Text); len(got) != 70 {
		t.Fatalf("%d words survived, want 70", len(got))
	}
	// Short words never consult the tokenizer for the cap: they cannot reach it.
	calls := &countingCounter{inner: fakeCounter{perWord: 1}}
	Chunk(speech(119, "ok"), calls)
	if calls.n != 1 {
		t.Fatalf("tokenized %d times for one short passage, want once (its count)", calls.n)
	}
}

type countingCounter struct {
	inner TokenCounter
	n     int
}

func (c *countingCounter) Encode(prefix, text string) []int64 {
	c.n++
	return c.inner.Encode(prefix, text)
}

// A single word over the cap, which no word boundary can split, is embedded
// whole in pieces that each fit and count as one word, so none can support a
// topic on its own.
func TestChunkSplitsASingleOverlongWord(t *testing.T) {
	long := strings.Repeat("é", 1200)
	words := append(speech(3, "short"), Word{Text: long, StartMS: 400, EndMS: 900, Speaker: "a"})
	passages := Chunk(words, fakeCounter{perRune: 1})
	var text strings.Builder
	for _, p := range passages[1:] {
		if p.TokenCount > MaxTokens || p.WordCount != 1 {
			t.Fatalf("piece %+v", p)
		}
		text.WriteString(p.Text)
	}
	if passages[0].Text != "short short short" || passages[0].Boundary != boundaryTokenCap {
		t.Fatalf("first passage %+v", passages[0])
	}
	if text.String() != long || len(passages) != 4 || passages[1].Boundary != boundaryLongSplit || passages[3].Boundary != boundaryEnd {
		t.Fatalf("%d passages; the pieces do not rebuild the word", len(passages))
	}
}

func TestChunkStripsLikePython(t *testing.T) {
	words := []Word{{Text: "\x1chello　", StartMS: 0, EndMS: 10}, {Text: "\x1f", StartMS: 20, EndMS: 30}, {Text: "end.\n", StartMS: 40, EndMS: 50}}
	p := Chunk(words, fakeCounter{perWord: 1})
	if len(p) != 1 || p[0].Text != "hello end." || p[0].WordCount != 2 {
		t.Fatalf("%+v", p)
	}
	if !sentenceEnd.MatchString("end.\n") || !sentenceEnd.MatchString(`so?”`) || sentenceEnd.MatchString("end. ") || sentenceEnd.MatchString("e.g") {
		t.Fatal("sentence end differs from the prototype's")
	}
}

// With the real tokenizer, text that costs many tokens per word (CJK, emoji,
// combining marks, byte-fallback symbols) is split at the cap: every passage
// fits, and the model accepts every one of them.
func TestChunkRespectsTheCapWithTheTokenizer(t *testing.T) {
	files := modelFiles(t)
	tok, err := LoadTokenizer(files.Tokenizer)
	if err != nil {
		t.Fatal(err)
	}
	heavy := []string{"週次の計画会議では未解決のサポート依頼を確認しました", "🚀👨‍👩‍👧‍👦🇮🇹🏳️‍🌈", "Z͓͑͒a͔͕l͖g͗o", "𐀀𓀀🧿ꙮ", "▁▁▁▁", "١٢٣٤٥٦٧٨٩"}
	var words []Word
	for i := range 600 {
		words = append(words, Word{Text: heavy[i%len(heavy)], StartMS: int64(i) * 100, EndMS: int64(i)*100 + 90, Speaker: "a"})
	}
	passages := Chunk(words, tok)
	capped := 0
	for _, p := range passages {
		if p.TokenCount > MaxTokens || p.TokenCount != len(tok.Encode(PassagePrefix, p.Text)) {
			t.Fatalf("passage %d: %d tokens, its text %d", p.Ordinal, p.TokenCount, len(tok.Encode(PassagePrefix, p.Text)))
		}
		if p.Boundary == boundaryTokenCap {
			capped++
		}
	}
	if capped == 0 {
		t.Fatal("the cap never fired; the test proves nothing")
	}
	e, err := Open(files, Options{Threads: 2, Batch: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	texts := make([]string, len(passages))
	for i, p := range passages {
		texts[i] = p.Text
	}
	if _, err := e.EmbedTexts(context.Background(), PassagePrefix, texts); err != nil {
		t.Fatalf("the model refused a chunked passage: %v", err)
	}
}
