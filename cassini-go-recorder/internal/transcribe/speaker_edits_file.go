package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
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

// SHA256 is the SHA-256 of the file Write writes: what identifies the
// transcript a summary was written from.
func (t TranscriptSpeakers) SHA256() (string, error) {
	data, err := json.MarshalIndent(t.file, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(data, '\n'))
	return hex.EncodeToString(sum[:]), nil
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

// Summarize asks the summary LLM for a new meeting summary of this transcript,
// labelled from its roster, with low-confidence words left out exactly as the
// build leaves them out.
func (t TranscriptSpeakers) Summarize(cfg LLMConfig) (string, error) {
	streams := make([]AudioStream, len(t.file.Speakers))
	for i, s := range t.file.Speakers {
		streams[i] = AudioStream{Index: -1, SpeakerID: s.ID, SpeakerLabel: s.Label}
	}
	segments, _ := WithoutLowConfidenceWords(t.Segments())
	return buildMeetingSummaryFn(cfg, streams, segments)
}

// WithLabels returns a copy in which every listed speaker that roster names
// is called by the label roster gives it. Segments, words and the order of
// the speakers are kept exactly: a rename changes speakers[].label and
// nothing else.
func (t TranscriptSpeakers) WithLabels(roster []RosterEntry) TranscriptSpeakers {
	labels := make(map[string]string, len(roster))
	for _, r := range roster {
		labels[r.ID] = r.Label
	}
	out := TranscriptSpeakers{file: t.file}
	out.file.Speakers = make([]speakerEntry, len(t.file.Speakers))
	for i, s := range t.file.Speakers {
		out.file.Speakers[i] = s
		if label, ok := labels[s.ID]; ok {
			out.file.Speakers[i].Label = label
		}
	}
	return out
}
