package topics

import (
	"strings"
	"testing"
)

// Public text only: UDHR Article 1 (model_test.go) and synthetic speech.
var languageSamples = udhrArticle1

var speechSamples = map[string]string{
	"en": "Uh so the the release candidate is uh mostly ready, we just need to fix the the upload retry and then I think we can ship it on Monday.",
	"it": "Allora, ehm, la versione nuova è quasi pronta, manca solo da sistemare il il caricamento e poi la rilasciamo lunedì.",
}

func passageOf(text string) Passage {
	return Passage{Text: text, WordCount: len(strings.Fields(text))}
}

func TestDetectLanguageOnPublicText(t *testing.T) {
	for want, text := range languageSamples {
		// A meeting of a few passages in one language.
		got := DetectLanguage([]Passage{passageOf(text), passageOf(text), passageOf(text)})
		if got.Code != want || got.Confidence < LanguageConfidenceFloor {
			t.Errorf("%s: got %+v", want, got)
		}
	}
	for want, text := range speechSamples {
		if got := DetectLanguage([]Passage{passageOf(text)}); got.Code != want {
			t.Errorf("speech %s: got %+v", want, got)
		}
	}
}

// Mostly one language with an aside in another is that language; an even mix
// is unknown, and so is a meeting with nothing long enough to tell.
func TestDetectLanguageVotesByWords(t *testing.T) {
	en, it := passageOf(languageSamples["en"]), passageOf(languageSamples["it"])
	got := DetectLanguage([]Passage{en, en, en, it})
	if got.Code != "en" || got.Confidence < 0.7 || got.Confidence > 0.8 {
		t.Fatalf("mostly English: %+v", got)
	}
	if got := DetectLanguage([]Passage{en, it, en, it}); got.Code != LanguageUnknown || got.Confidence >= LanguageConfidenceFloor {
		t.Fatalf("even mix: %+v", got)
	}
	short := Passage{Text: "ok yeah sure", WordCount: 3}
	if got := DetectLanguage([]Passage{short, short}); got.Code != LanguageUnknown || got.Confidence != 0 {
		t.Fatalf("too short: %+v", got)
	}
	noise := passageOf("1234 5678 9012 3456 7890 1234 5678 9012 3456 7890 1234 5678 9012")
	if got := DetectLanguage([]Passage{noise}); got.Code != LanguageUnknown {
		t.Fatalf("numbers: %+v", got)
	}
	// Short passages neither vote nor dilute the share.
	if got := DetectLanguage([]Passage{en, short, short, short}); got.Code != "en" || got.Confidence != 1 {
		t.Fatalf("with short asides: %+v", got)
	}
}

func TestDetectLanguageIsDeterministic(t *testing.T) {
	passages := []Passage{passageOf(languageSamples["fr"]), passageOf(languageSamples["es"]), passageOf(languageSamples["it"])}
	first := DetectLanguage(passages)
	for range 50 {
		if got := DetectLanguage(passages); got != first {
			t.Fatalf("%+v then %+v", first, got)
		}
	}
}
