package cassini

import (
	"strings"

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/transcribe"
)

// Prose derivation constants, chosen to land close to what the viewer renders
// for the same meeting (cassini-viewer/src/viewer/portable.ts): a ~2.2s pause
// ends a paragraph, and a paragraph is capped so a monologue still breaks up.
// They only affect where paragraphs break, never which words appear.
const (
	proseGapThresholdMS = 2200
	proseMaxWords       = 96
)

// transcriptSourceDerived labels prose that this CLI assembled from word
// timings rather than read from an LLM-cleaned transcript.
//
// It is load-bearing, not decoration. A portable .opus does not currently carry
// a readable transcript at all — the producer drops it (see followups) — so
// every consumer gets derived prose, and an agent must not present it as
// cleaned-up text. The viewer marks its equivalent fallback the same way.
const transcriptSourceDerived = "derived-from-words"

// proseSegment is one speaker-attributed paragraph of derived prose.
type proseSegment struct {
	Speaker      string
	SpeakerLabel string
	StartMS      int64
	EndMS        int64
	Text         string
}

// deriveProseSegments turns the word-level transcript recovered from a portable
// .opus into readable, speaker-attributed paragraphs.
//
// Word-level items are all a published .opus carries: the pack step explodes
// every source segment into one item per word, keeping only the speaker id
// (flattenPortableTranscriptItems), so segment boundaries have to be rebuilt
// from the timings. Words are first split into runs of consecutive words
// sharing a speaker — a speaker change always ends a paragraph — and each run
// is grouped by the same gap/length rule the transcribe pipeline uses, so
// derived prose and pipeline-assembled prose break in the same places.
//
// Output order follows word order, which is already chronological, so no
// re-sort is needed.
func deriveProseSegments(words []inspectpkg.TranscriptWord, speakerLabels map[string]string) []proseSegment {
	segments := make([]proseSegment, 0, len(words)/proseMaxWords+1)

	for _, run := range splitWordRunsBySpeaker(words) {
		assembled := transcribe.AssembleSegments(run.speaker, run.words, proseGapThresholdMS, proseMaxWords)
		for _, segment := range assembled {
			text := strings.TrimSpace(segment.Text)
			if text == "" {
				continue
			}
			segments = append(segments, proseSegment{
				Speaker:      run.speaker,
				SpeakerLabel: speakerLabel(run.speaker, speakerLabels),
				StartMS:      segment.StartMS,
				EndMS:        segment.EndMS,
				Text:         text,
			})
		}
	}
	return segments
}

// speakerWordRun is a maximal stretch of consecutive words spoken by one
// speaker.
type speakerWordRun struct {
	speaker string
	words   []transcribe.Word
}

// splitWordRunsBySpeaker groups consecutive words by speaker id, preserving
// order. transcribe.AssembleSegments takes a single speaker id for the whole
// call, so a mixed-speaker word list has to be split before grouping or every
// paragraph would be attributed to whoever spoke first.
func splitWordRunsBySpeaker(words []inspectpkg.TranscriptWord) []speakerWordRun {
	runs := make([]speakerWordRun, 0, 8)
	for _, word := range words {
		if strings.TrimSpace(word.Text) == "" {
			continue
		}
		converted := transcribe.Word{Text: word.Text, StartMS: word.StartMS, EndMS: word.EndMS}
		if len(runs) > 0 && runs[len(runs)-1].speaker == word.Speaker {
			runs[len(runs)-1].words = append(runs[len(runs)-1].words, converted)
			continue
		}
		runs = append(runs, speakerWordRun{speaker: word.Speaker, words: []transcribe.Word{converted}})
	}
	return runs
}

// speakerLabel resolves a speaker id to its display label, falling back to the
// raw id so an unlabelled speaker is still attributed rather than dropped.
func speakerLabel(id string, labels map[string]string) string {
	if label := strings.TrimSpace(labels[id]); label != "" {
		return label
	}
	return strings.TrimSpace(id)
}
