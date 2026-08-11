package cassini

import (
	"strings"
	"testing"

	inspectpkg "gocassini/internal/inspect"
)

// wordsAt builds a word run for one speaker starting at startMS, spacing words
// stepMS apart so the gap between them stays below proseGapThresholdMS.
func wordsAt(speaker string, startMS int64, stepMS int64, texts ...string) []inspectpkg.TranscriptWord {
	words := make([]inspectpkg.TranscriptWord, 0, len(texts))
	at := startMS
	for _, text := range texts {
		words = append(words, inspectpkg.TranscriptWord{
			Text:    text,
			StartMS: at,
			EndMS:   at + 100,
			Speaker: speaker,
		})
		at += stepMS
	}
	return words
}

func TestDeriveProseSegmentsJoinsOneSpeakerIntoOneParagraph(t *testing.T) {
	words := wordsAt("spk1", 0, 200, "we", "should", "ship", "it")

	got := deriveProseSegments(words, map[string]string{"spk1": "Erlich"})

	if len(got) != 1 {
		t.Fatalf("got %d segments, want 1: %+v", len(got), got)
	}
	if want := "we should ship it"; got[0].Text != want {
		t.Errorf("Text = %q, want %q", got[0].Text, want)
	}
	if got[0].SpeakerLabel != "Erlich" {
		t.Errorf("SpeakerLabel = %q, want %q", got[0].SpeakerLabel, "Erlich")
	}
	if got[0].StartMS != 0 {
		t.Errorf("StartMS = %d, want 0", got[0].StartMS)
	}
	if want := int64(700); got[0].EndMS != want {
		t.Errorf("EndMS = %d, want %d", got[0].EndMS, want)
	}
}

// A speaker change must always end a paragraph, even with no pause between the
// words — otherwise the whole exchange is attributed to whoever spoke first.
func TestDeriveProseSegmentsSplitsOnSpeakerChange(t *testing.T) {
	words := append(
		wordsAt("spk1", 0, 200, "shall", "we"),
		wordsAt("spk2", 400, 200, "yes", "absolutely")...,
	)

	got := deriveProseSegments(words, map[string]string{"spk1": "Erlich", "spk2": "Monica"})

	if len(got) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(got), got)
	}
	if got[0].Text != "shall we" || got[0].SpeakerLabel != "Erlich" {
		t.Errorf("segment 0 = %q by %q, want %q by %q", got[0].Text, got[0].SpeakerLabel, "shall we", "Erlich")
	}
	if got[1].Text != "yes absolutely" || got[1].SpeakerLabel != "Monica" {
		t.Errorf("segment 1 = %q by %q, want %q by %q", got[1].Text, got[1].SpeakerLabel, "yes absolutely", "Monica")
	}
}

// Returning to a speaker after an interjection starts a new paragraph rather
// than merging back into the earlier one, so the exchange reads in order.
func TestDeriveProseSegmentsKeepsAlternatingSpeakersInOrder(t *testing.T) {
	words := wordsAt("spk1", 0, 200, "one")
	words = append(words, wordsAt("spk2", 200, 200, "two")...)
	words = append(words, wordsAt("spk1", 400, 200, "three")...)

	got := deriveProseSegments(words, nil)

	if len(got) != 3 {
		t.Fatalf("got %d segments, want 3: %+v", len(got), got)
	}
	texts := []string{got[0].Text, got[1].Text, got[2].Text}
	if want := "one two three"; strings.Join(texts, " ") != want {
		t.Errorf("segments in order = %q, want %q", strings.Join(texts, " "), want)
	}
	if got[0].Speaker != "spk1" || got[1].Speaker != "spk2" || got[2].Speaker != "spk1" {
		t.Errorf("speakers = %q/%q/%q, want spk1/spk2/spk1", got[0].Speaker, got[1].Speaker, got[2].Speaker)
	}
}

// A pause longer than the threshold ends a paragraph for the same speaker.
func TestDeriveProseSegmentsSplitsOnLongPause(t *testing.T) {
	words := wordsAt("spk1", 0, 200, "before", "the", "break")
	words = append(words, wordsAt("spk1", 10_000, 200, "after", "the", "break")...)

	got := deriveProseSegments(words, nil)

	if len(got) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(got), got)
	}
	if got[0].Text != "before the break" {
		t.Errorf("segment 0 = %q, want %q", got[0].Text, "before the break")
	}
	if got[1].Text != "after the break" {
		t.Errorf("segment 1 = %q, want %q", got[1].Text, "after the break")
	}
}

// A monologue is capped so an agent never receives one unbroken wall of text.
func TestDeriveProseSegmentsCapsParagraphLength(t *testing.T) {
	texts := make([]string, 0, proseMaxWords*2)
	for i := 0; i < proseMaxWords*2; i++ {
		texts = append(texts, "word")
	}
	words := wordsAt("spk1", 0, 150, texts...)

	got := deriveProseSegments(words, nil)

	if len(got) != 2 {
		t.Fatalf("got %d segments, want 2 (capped at %d words each): %+v", len(got), proseMaxWords, got)
	}
	for i, segment := range got {
		if fields := len(strings.Fields(segment.Text)); fields > proseMaxWords {
			t.Errorf("segment %d has %d words, want <= %d", i, fields, proseMaxWords)
		}
	}
}

// An unlabelled speaker keeps its raw id, so attribution degrades rather than
// disappearing.
func TestDeriveProseSegmentsFallsBackToSpeakerID(t *testing.T) {
	got := deriveProseSegments(wordsAt("spk9", 0, 200, "hello"), map[string]string{"spk1": "Erlich"})

	if len(got) != 1 {
		t.Fatalf("got %d segments, want 1", len(got))
	}
	if got[0].SpeakerLabel != "spk9" {
		t.Errorf("SpeakerLabel = %q, want the raw id %q", got[0].SpeakerLabel, "spk9")
	}
}

func TestDeriveProseSegmentsHandlesEmptyAndBlankInput(t *testing.T) {
	if got := deriveProseSegments(nil, nil); len(got) != 0 {
		t.Errorf("deriveProseSegments(nil) = %+v, want no segments", got)
	}

	blank := []inspectpkg.TranscriptWord{
		{Text: "   ", StartMS: 0, EndMS: 100, Speaker: "spk1"},
		{Text: "", StartMS: 100, EndMS: 200, Speaker: "spk1"},
	}
	if got := deriveProseSegments(blank, nil); len(got) != 0 {
		t.Errorf("deriveProseSegments(blank words) = %+v, want no segments", got)
	}
}
