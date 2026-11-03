package topics

import (
	"github.com/abadojack/whatlanggo"
)

// DetectorVersion names the language detector and its rules below. The scorer
// treats a meeting whose recorded detector version is not the one it expects
// as language unknown, so a change here must bump it.
const DetectorVersion = "whatlanggo-1.0.1/vote-1"

// LanguageUnknown is the language of a meeting the detector is not sure about.
const LanguageUnknown = "unknown"

// The vote. whatlanggo's own confidence only compares the best language with
// the runner-up on one text, and it is near 1 even on a text that is half one
// language and half another. So each passage of at least MinSupportingWords
// words votes for its language with its word count, a passage the detector is
// not sure about votes for nobody, and the meeting's confidence is the winning
// language's share of all the voting passages' words.
const (
	// passageConfidenceFloor is the detector confidence a passage needs to vote.
	passageConfidenceFloor = 0.5
	// LanguageConfidenceFloor is the share of words below which a meeting's
	// language is unknown: a meeting split evenly between two languages, or
	// mostly too short or too noisy to tell, falls under it.
	LanguageConfidenceFloor = 0.6
)

// Language is a detected language: an ISO 639-1 code (or the ISO 639-3 code
// for a language without one), or LanguageUnknown.
type Language struct {
	Code       string
	Confidence float64
}

// DetectLanguage identifies the language of a meeting from its passages.
// Production builds record no transcript language
// (docs/proposals/automatic-tagging, "Embedding"), so the embed detects it.
// The same passages always give the same answer.
func DetectLanguage(passages []Passage) Language {
	votes := map[string]int{}
	total := 0
	for _, p := range passages {
		if p.WordCount < MinSupportingWords {
			continue
		}
		total += p.WordCount
		if code, ok := passageLanguage(p.Text); ok {
			votes[code] += p.WordCount
		}
	}
	best, bestWords := "", 0
	for code, words := range votes {
		// Ties go to the smaller code, so map order never decides.
		if words > bestWords || (words == bestWords && code < best) {
			best, bestWords = code, words
		}
	}
	if total == 0 || bestWords == 0 {
		return Language{Code: LanguageUnknown}
	}
	share := float64(bestWords) / float64(total)
	if share < LanguageConfidenceFloor {
		return Language{Code: LanguageUnknown, Confidence: share}
	}
	return Language{Code: best, Confidence: share}
}

func passageLanguage(text string) (string, bool) {
	info := whatlanggo.Detect(text)
	if info.Lang < 0 || info.Confidence < passageConfidenceFloor {
		return "", false
	}
	if code := info.Lang.Iso6391(); code != "" {
		return code, true
	}
	return info.Lang.Iso6393(), true
}
