package topics

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkerVersion names the passage rules below. A change to any of them must
// bump it: stored passages made under other rules cover other words.
const ChunkerVersion = "natural-1"

// The natural-passage rules, ported from the prototype's natural_chunks
// (docs/proposals/automatic-tagging, "Chunking").
const (
	minPassageWords   = 25   // before a pause or a speaker change may close a passage
	pauseMS           = 2000 // a gap to the next word that closes a passage
	minSentenceWords  = 40   // before a sentence end may close a passage
	maxPassageWords   = 120  // the word safety cap
	maxTokenCapBytes  = MaxTokens - 2 - len(PromptPrefix)
	boundaryEnd       = "end"
	boundaryPause     = "pause"
	boundarySpeaker   = "speaker change"
	boundarySentence  = "sentence end"
	boundaryWordCap   = "word safety cap"
	boundaryTokenCap  = "token cap"
	boundaryLongSplit = "long word split"
)

// MinSupportingWords is the shortest passage that may support a topic or vote
// for a meeting's language. The chunker keeps shorter passages, so every word
// is in one; the scorer and the language vote skip them.
const MinSupportingWords = 12

// sentenceEnd is the prototype's `[.!?]["'’”]*$` with Python's meaning of
// `$` (the end, or before a final newline), on the word as transcribed.
var sentenceEnd = regexp.MustCompile(`[.!?]["'’”]*\n?$`)

// trimSpace strips what Python's str.strip strips: Go's Unicode spaces plus
// the four ASCII separators U+001C..U+001F. The prototype stripped words with
// it, and passages must keep its exact text.
func trimSpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
}

// Passage is a run of consecutive words of one transcript. Text is what the
// model embeds; it never leaves the CLI.
type Passage struct {
	Ordinal    int
	StartMS    int64
	EndMS      int64
	WordCount  int
	TokenCount int
	Boundary   string
	Text       string
}

// Chunk splits a transcript into natural passages. It drops empty words,
// orders the rest by (start, end, position), and closes a passage at the
// first rule that matches: the last word; 25 words and a pause of 2 s to the
// next; 25 words and a different next speaker; 40 words and a word that ends
// a sentence; 120 words. It also closes a passage at the word before the
// passage would exceed MaxTokens: it splits, never truncates. Every word is
// in exactly one passage, in order; the same words always give the same
// passages.
func Chunk(words []Word, tok TokenCounter) []Passage {
	kept := make([]Word, 0, len(words))
	for _, w := range words {
		if trimSpace(w.Text) != "" {
			kept = append(kept, w)
		}
	}
	// Stable: equal times keep their transcript order.
	sort.SliceStable(kept, func(a, b int) bool {
		if kept[a].StartMS != kept[b].StartMS {
			return kept[a].StartMS < kept[b].StartMS
		}
		return kept[a].EndMS < kept[b].EndMS
	})
	c := chunker{tok: tok}
	for i, word := range kept {
		if c.wouldExceed(word) {
			c.close(boundaryTokenCap)
		}
		c.add(word)
		var next *Word
		if i+1 < len(kept) {
			next = &kept[i+1]
		}
		n := len(c.pending)
		switch {
		case next == nil:
			c.close(boundaryEnd)
		case n >= minPassageWords && next.StartMS-word.EndMS >= pauseMS:
			c.close(boundaryPause)
		case n >= minPassageWords && next.Speaker != word.Speaker:
			c.close(boundarySpeaker)
		case n >= minSentenceWords && sentenceEnd.MatchString(word.Text):
			c.close(boundarySentence)
		case n >= maxPassageWords:
			c.close(boundaryWordCap)
		}
	}
	return c.out
}

type chunker struct {
	tok     TokenCounter
	pending []Word
	text    strings.Builder
	out     []Passage
}

func (c *chunker) add(w Word) {
	if len(c.pending) > 0 {
		c.text.WriteByte(' ')
	}
	c.text.WriteString(trimSpace(w.Text))
	c.pending = append(c.pending, w)
}

// wouldExceed reports whether adding w would take the pending passage over
// MaxTokens. A token covers at least one byte of its text (the tokenizer only
// replaces spaces, and byte fallback spends one token per byte), so a text no
// longer than the remaining bytes always fits and is not tokenized.
func (c *chunker) wouldExceed(w Word) bool {
	if len(c.pending) == 0 {
		return false
	}
	candidate := c.text.String() + " " + trimSpace(w.Text)
	if len(candidate) <= maxTokenCapBytes {
		return false
	}
	return len(c.tok.Encode(candidate)) > MaxTokens
}

func (c *chunker) close(boundary string) {
	if len(c.pending) == 0 {
		return
	}
	p := Passage{
		StartMS:   c.pending[0].StartMS,
		EndMS:     c.pending[0].EndMS,
		WordCount: len(c.pending),
		Boundary:  boundary,
		Text:      c.text.String(),
	}
	for _, w := range c.pending {
		p.StartMS = min(p.StartMS, w.StartMS)
		p.EndMS = max(p.EndMS, w.EndMS)
	}
	c.pending = c.pending[:0]
	c.text.Reset()
	if p.TokenCount = len(c.tok.Encode(p.Text)); p.TokenCount > MaxTokens {
		// Only a single word can get here: anything longer was split before
		// it grew past the cap. Split the word at rune boundaries so its text
		// is embedded whole rather than refused or cut.
		c.splitLongWord(p)
		return
	}
	p.Ordinal = len(c.out)
	c.out = append(c.out, p)
}

// splitLongWord embeds one over-long word as several passages, each within
// MaxTokens, in order. Each piece counts as one word, so none of them can
// ever support a topic on its own.
func (c *chunker) splitLongWord(p Passage) {
	rest := p.Text
	for rest != "" {
		// The longest rune prefix that fits; at least one rune.
		lo, hi := 1, utf8.RuneCountInString(rest)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if len(c.tok.Encode(runePrefix(rest, mid))) <= MaxTokens {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		piece := runePrefix(rest, lo)
		rest = rest[len(piece):]
		q := p
		q.Text, q.WordCount, q.Boundary = piece, 1, boundaryLongSplit
		if rest == "" {
			q.Boundary = p.Boundary
		}
		q.TokenCount = len(c.tok.Encode(piece))
		q.Ordinal = len(c.out)
		c.out = append(c.out, q)
	}
}

func runePrefix(s string, n int) string {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}
