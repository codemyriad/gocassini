package operator

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Draft names for recurring-topic proposals (docs/proposals/automatic-
// tagging, "Recurring-topic proposals", step 6). No built-in stop list and no
// language rules: a term that is everywhere in the members' meetings is
// dropped because it is everywhere, and what remains is weighed by how much
// more often the group uses it.

const (
	topicNameMinRunes  = 3
	topicNameHints     = 5
	topicNameWords     = 2
	topicNameCommonCut = 0.5
)

// topicNameText is one passage of a member meeting, for naming.
type topicNameText struct {
	Meeting string
	Text    string
	// InGroup: the passage is one of the proposal's own.
	InGroup bool
}

// topicTerms splits text into terms: runs of letters, digits and marks
// (Indic vowel signs are marks) of at least three runes, case-folded. The
// recorder's topics.SpeakerTermHashes uses the same rule; change both.
func topicTerms(text string) []string {
	var out []string
	var b strings.Builder
	runes := 0
	flush := func() {
		if runes >= topicNameMinRunes {
			out = append(out, strings.ToLower(b.String()))
		}
		b.Reset()
		runes = 0
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			b.WriteRune(r)
			runes++
			continue
		}
		flush()
	}
	flush()
	return out
}

// draftTopicName names a group from its passages, against every passage of
// the member meetings. exclude holds terms never to use (the members' own
// speaker labels). It returns the draft name ("a / b") and up to five hints;
// "" when nothing stands out.
func draftTopicName(texts []topicNameText, exclude func(term string) bool) (string, []string) {
	n := len(texts)
	if n == 0 {
		return "", nil
	}
	df := map[string]int{}
	inGroup := map[string]int{}
	meetings := map[string]map[string]bool{}
	for _, t := range texts {
		seen := map[string]bool{}
		for _, term := range topicTerms(t.Text) {
			if t.InGroup {
				inGroup[term]++
				if meetings[term] == nil {
					meetings[term] = map[string]bool{}
				}
				meetings[term][t.Meeting] = true
			}
			if !seen[term] {
				seen[term] = true
				df[term]++
			}
		}
	}
	type scored struct {
		term  string
		score float64
	}
	var candidates []scored
	for term, count := range inGroup {
		if (exclude != nil && exclude(term)) || len(meetings[term]) < 2 {
			continue
		}
		if float64(df[term]) > topicNameCommonCut*float64(n) {
			continue
		}
		candidates = append(candidates, scored{term, float64(count) * math.Log(float64(n)/float64(df[term]))})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].term < candidates[j].term
	})
	var hints []string
	for _, c := range candidates {
		if c.score <= 0 {
			break
		}
		overlaps := false
		for _, h := range hints {
			if strings.Contains(h, c.term) || strings.Contains(c.term, h) {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		hints = append(hints, c.term)
		if len(hints) == topicNameHints {
			break
		}
	}
	if len(hints) == 0 {
		return "", nil
	}
	name := hints[:min(topicNameWords, len(hints))]
	return strings.Join(name, " / "), hints
}

// sharedTopicTerms lists the terms of typed text that also occur in a draft
// name or its hints: words that came from meetings.
func sharedTopicTerms(typed string, draft []string) []string {
	from := map[string]bool{}
	for _, d := range draft {
		for _, term := range topicTerms(d) {
			from[term] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, term := range topicTerms(typed) {
		if from[term] && !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	return out
}

// speakerTermExcluder says whether a term is a word of a speaker's name,
// given the keyed hashes the embed reported (HMAC-SHA256 with the
// deployment's speaker key; topics.SpeakerTermHashes in the recorder).
func speakerTermExcluder(key []byte, hashes map[string]bool) func(string) bool {
	return func(term string) bool {
		if len(key) == 0 || len(hashes) == 0 {
			return false
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(term))
		return hashes[hex.EncodeToString(mac.Sum(nil))]
	}
}
