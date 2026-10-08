package transcribe

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// TranscriptSpeakers is the roster and segments of a transcript.words.v1.json
// file, enough to apply speaker edits and write the result back.
type TranscriptSpeakers struct {
	file transcriptFile
}

// ReadTranscriptSpeakers loads transcript.words.v1.json.
func ReadTranscriptSpeakers(path string) (TranscriptSpeakers, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return TranscriptSpeakers{}, err
	}
	var t TranscriptSpeakers
	if err := json.Unmarshal(b, &t.file); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	if t.file.Version != transcriptWordsVersion {
		return t, fmt.Errorf("%s: unsupported version %q", path, t.file.Version)
	}
	return t, nil
}

// Roster returns the speakers listed in the file.
func (t TranscriptSpeakers) Roster() []RosterEntry {
	out := make([]RosterEntry, len(t.file.Speakers))
	for i, s := range t.file.Speakers {
		out[i] = RosterEntry{ID: s.ID, Label: s.Label}
	}
	return out
}

// Segments converts the file's segments back to pipeline segments, keeping
// each word's attribution evidence.
func (t TranscriptSpeakers) Segments() []Segment {
	segs := make([]Segment, len(t.file.Segments))
	for i, s := range t.file.Segments {
		words := make([]Word, len(s.Words))
		for j, w := range s.Words {
			words[j] = Word{Text: w.Text, StartMS: w.StartMS, EndMS: w.EndMS, LowConfidenceSpeaker: w.LowConfidenceSpeaker}
			if w.AttributionGapDB != nil {
				words[j].AttributionGapDB = *w.AttributionGapDB
				words[j].HasAttributionGap = true
			}
		}
		segs[i] = Segment{SpeakerID: s.Speaker, StartMS: s.StartMS, EndMS: s.EndMS, Text: s.Text, Words: words}
	}
	return segs
}

// WithSpeakers returns a copy whose segments and roster are replaced. Media
// stays as it was: the audio is never touched by a speaker edit.
func (t TranscriptSpeakers) WithSpeakers(segments []Segment, roster []RosterEntry) (TranscriptSpeakers, error) {
	if err := ValidateSegments(segments); err != nil {
		return t, err
	}
	out := TranscriptSpeakers{file: t.file}
	out.file.Speakers = make([]speakerEntry, len(roster))
	for i, r := range roster {
		out.file.Speakers[i] = speakerEntry{ID: r.ID, Label: r.Label}
	}
	out.file.Segments = buildTranscriptSegmentEntries(segments)
	return out, nil
}

// Write writes the file atomically.
func (t TranscriptSpeakers) Write(path string) error { return writeJSON(path, t.file) }

// WriteCaptionsVTT renders WebVTT captions labelled from the roster.
func (t TranscriptSpeakers) WriteCaptionsVTT(path string) error {
	streams := make([]AudioStream, len(t.file.Speakers))
	for i, s := range t.file.Speakers {
		streams[i] = AudioStream{Index: -1, SpeakerID: s.ID, SpeakerLabel: s.Label}
	}
	return WriteCaptionsVTT(path, streams, t.Segments())
}

// LogicalSpeakerCount counts people: participants, plus each voice of a
// split device in place of the device itself.
func (t TranscriptSpeakers) LogicalSpeakerCount() int {
	count := 0
	for _, s := range t.file.Speakers {
		if !strings.EqualFold(s.ID, "merged") {
			count++
		}
	}
	return count
}
