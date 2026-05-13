package transcribe

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"gocassini/internal/meetingtime"
)

// --- transcript.words.v1.json ---

const (
	transcriptWordsVersion    = "transcript.words.v1"
	readableTranscriptVersion = "transcript.readable.v1"
)

type transcriptFile struct {
	Version  string                   `json:"version"`
	Media    transcriptMedia          `json:"media"`
	Speakers []speakerEntry           `json:"speakers"`
	Segments []transcriptSegmentEntry `json:"segments"`
}

type readableTranscriptFile struct {
	Version                 string                 `json:"version"`
	Media                   transcriptMedia        `json:"media"`
	Speakers                []speakerEntry         `json:"speakers"`
	SourceTranscriptVersion string                 `json:"sourceTranscriptVersion,omitempty"`
	Segments                []readableSegmentEntry `json:"segments"`
}

type transcriptMedia struct {
	Src        string `json:"src"`
	DurationMS int64  `json:"durationMs"`
	SHA256     string `json:"sha256,omitempty"`
}

type speakerEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type transcriptSegmentEntry struct {
	ID      string      `json:"id"`
	Speaker string      `json:"speaker,omitempty"`
	StartMS int64       `json:"startMs"`
	EndMS   int64       `json:"endMs"`
	Text    string      `json:"text"`
	Words   []wordEntry `json:"words"`
}

type readableSegmentEntry struct {
	ID               string   `json:"id"`
	Speaker          string   `json:"speaker,omitempty"`
	StartMS          int64    `json:"startMs"`
	EndMS            int64    `json:"endMs"`
	Text             string   `json:"text"`
	SourceSegmentIDs []string `json:"sourceSegmentIds"`
}

type wordEntry struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
}

// WriteTranscriptJSON writes transcript.words.v1.json.
func WriteTranscriptJSON(path string, streams []AudioStream, segments []Segment, audioDurationMS int64) error {
	if err := ValidateSegments(segments); err != nil {
		return err
	}
	return writeJSON(path, buildTranscriptFile(streams, segments, audioDurationMS, ""))
}

// WriteReadableTranscriptJSON writes transcript.readable.v1.json using the
// readable transcript contract expected by the viewer.
func WriteReadableTranscriptJSON(path string, streams []AudioStream, segments []Segment, audioDurationMS int64) error {
	if err := ValidateSegments(segments); err != nil {
		return err
	}
	return writeJSON(path, buildReadableTranscriptFile(streams, segments, audioDurationMS, ""))
}

func buildTranscriptFile(streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string) transcriptFile {
	return transcriptFile{
		Version:  transcriptWordsVersion,
		Media:    transcriptMedia{Src: "meeting.webm", DurationMS: audioDurationMS, SHA256: sha256hex},
		Speakers: buildSpeakerEntries(streams, segments),
		Segments: buildTranscriptSegmentEntries(segments),
	}
}

func buildReadableTranscriptFile(streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string) readableTranscriptFile {
	return readableTranscriptFile{
		Version:                 readableTranscriptVersion,
		Media:                   transcriptMedia{Src: "meeting.webm", DurationMS: audioDurationMS, SHA256: sha256hex},
		Speakers:                buildSpeakerEntries(streams, segments),
		SourceTranscriptVersion: transcriptWordsVersion,
		Segments:                buildReadableSegmentEntries(segments),
	}
}

func buildSpeakerEntries(streams []AudioStream, segments []Segment) []speakerEntry {
	speakers := []speakerEntry{}
	seen := map[string]bool{}
	for _, seg := range segments {
		if seg.SpeakerID == "" || seen[seg.SpeakerID] {
			continue
		}
		seen[seg.SpeakerID] = true
		label := labelForSpeaker(seg.SpeakerID, streams)
		speakers = append(speakers, speakerEntry{ID: seg.SpeakerID, Label: label})
	}
	return speakers
}

func buildTranscriptSegmentEntries(segments []Segment) []transcriptSegmentEntry {
	entries := make([]transcriptSegmentEntry, len(segments))
	for segIndex, seg := range segments {
		segmentID := transcriptSegmentID(segIndex)
		words := make([]wordEntry, len(seg.Words))
		for wordIndex, w := range seg.Words {
			words[wordIndex] = wordEntry{
				ID:      transcriptWordID(segmentID, wordIndex),
				Text:    w.Text,
				StartMS: w.StartMS,
				EndMS:   w.EndMS,
			}
		}
		entries[segIndex] = transcriptSegmentEntry{
			ID:      segmentID,
			Speaker: seg.SpeakerID,
			StartMS: seg.StartMS,
			EndMS:   seg.EndMS,
			Text:    seg.Text,
			Words:   words,
		}
	}
	return entries
}

func buildReadableSegmentEntries(segments []Segment) []readableSegmentEntry {
	entries := make([]readableSegmentEntry, len(segments))
	for segIndex, seg := range segments {
		entries[segIndex] = readableSegmentEntry{
			ID:               readableSegmentID(segIndex),
			Speaker:          seg.SpeakerID,
			StartMS:          seg.StartMS,
			EndMS:            seg.EndMS,
			Text:             seg.Text,
			SourceSegmentIDs: []string{transcriptSegmentID(segIndex)},
		}
	}
	return entries
}

func transcriptSegmentID(index int) string {
	return fmt.Sprintf("seg_%06d", index)
}

func readableSegmentID(index int) string {
	return fmt.Sprintf("r_seg_%06d", index)
}

func transcriptWordID(segmentID string, wordIndex int) string {
	return fmt.Sprintf("%s:w_%d", segmentID, wordIndex)
}

// --- captions.vtt ---

// WriteCaptionsVTT writes a WebVTT captions file.
func WriteCaptionsVTT(path string, streams []AudioStream, segments []Segment) error {
	var sb strings.Builder
	sb.WriteString("WEBVTT\n\n")
	for _, seg := range segments {
		label := labelForSpeaker(seg.SpeakerID, streams)
		fmt.Fprintf(&sb, "%s --> %s\n", vttTime(seg.StartMS), vttTime(seg.EndMS))
		fmt.Fprintf(&sb, "<%s> %s\n\n", label, seg.Text)
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

func vttTime(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	ms2 := int(d.Milliseconds()) % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms2)
}

// --- manifest.json ---

type artifactManifest struct {
	Kind             string          `json:"kind"`
	Version          string          `json:"version"`
	GeneratedAt      string          `json:"generatedAt"`
	Source           artifactSource  `json:"source"`
	Files            artifactFiles   `json:"files"`
	SpeakerCount     int             `json:"speakerCount"`
	SegmentCount     int             `json:"segmentCount,omitempty"`
	DigestDurationMS int64           `json:"digestDurationMs,omitempty"`
	WordCount        int             `json:"wordCount"`
	Provenance       *provenanceInfo `json:"provenance,omitempty"`
}

type artifactSource struct {
	Basename        string `json:"basename"`
	DurationMS      int64  `json:"durationMs"`
	RecordedAtLocal string `json:"recordedAtLocal,omitempty"`
}

type artifactFiles struct {
	Audio              string `json:"audio"`
	Transcript         string `json:"transcript"`
	ReadableTranscript string `json:"readableTranscript,omitempty"`
	Captions           string `json:"captions,omitempty"`
	Summary            string `json:"summary,omitempty"`
}

type provenanceInfo struct {
	SpeechToText    *provStep `json:"speechToText,omitempty"`
	ReadableCleanup *provStep `json:"readableCleanup,omitempty"`
	MeetingSummary  *provStep `json:"meetingSummary,omitempty"`
}

type provStep struct {
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	Device  string `json:"device,omitempty"`
}

// WriteManifest writes manifest.json summarising the build.
func WriteManifest(path, srcBasename string, srcDurationMS int64, streams []AudioStream, segments []Segment, sttModelID ModelID, llmModel string, hasReadable bool, summaryModel string, hasSummary bool) error {
	wordCount := 0
	for _, seg := range segments {
		wordCount += len(seg.Words)
	}

	files := artifactFiles{
		Audio:      "meeting.webm",
		Transcript: "transcript.words.v1.json",
	}
	if hasReadable {
		files.ReadableTranscript = "transcript.readable.v1.json"
		files.Captions = "captions.vtt"
	}
	if hasSummary {
		files.Summary = "summary.md"
	}

	prov := &provenanceInfo{
		SpeechToText: &provStep{
			Backend: "sherpa-onnx",
			Model:   string(sttModelID),
		},
	}
	if hasReadable {
		prov.ReadableCleanup = &provStep{
			Backend: "openai-compatible",
			Model:   llmModel,
		}
	}
	if hasSummary {
		prov.MeetingSummary = &provStep{
			Backend: "openai-compatible",
			Model:   summaryModel,
		}
	}

	doc := artifactManifest{
		Kind:        "cassini.meeting-artifact.v1",
		Version:     "1",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Source: artifactSource{
			Basename:        srcBasename,
			DurationMS:      srcDurationMS,
			RecordedAtLocal: meetingtime.InferRecordedAtLocal(srcBasename),
		},
		Files:            files,
		SpeakerCount:     len(streams),
		SegmentCount:     len(segments),
		DigestDurationMS: srcDurationMS,
		WordCount:        wordCount,
		Provenance:       prov,
	}
	return writeJSON(path, doc)
}

// --- segment assembly ---

// AssembleSegments groups words from a speaker into logical segments.
// A new segment starts when there is a gap > gapThresholdMS or the
// segment exceeds maxWords.
func AssembleSegments(speakerID string, words []Word, gapThresholdMS int64, maxWords int) []Segment {
	if gapThresholdMS <= 0 {
		gapThresholdMS = 1500
	}
	if maxWords <= 0 {
		maxWords = 60
	}

	var segments []Segment
	var cur []Word

	flush := func() {
		if len(cur) == 0 {
			return
		}
		text := wordsToText(cur)
		segments = append(segments, Segment{
			SpeakerID: speakerID,
			StartMS:   cur[0].StartMS,
			EndMS:     cur[len(cur)-1].EndMS,
			Text:      text,
			Words:     append([]Word(nil), cur...),
		})
		cur = cur[:0]
	}

	for _, w := range words {
		if len(cur) > 0 {
			gap := w.StartMS - cur[len(cur)-1].EndMS
			if gap > gapThresholdMS || len(cur) >= maxWords {
				flush()
			}
		}
		cur = append(cur, w)
	}
	flush()
	return segments
}

func wordsToText(words []Word) string {
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.Text
	}
	return strings.Join(parts, " ")
}

// MergeAndSortSegments merges per-speaker segment lists and sorts by start time.
func MergeAndSortSegments(perSpeaker [][]Segment) []Segment {
	var all []Segment
	for _, segs := range perSpeaker {
		all = append(all, segs...)
	}
	sortSegments(all)
	return all
}

func sortSegments(segs []Segment) {
	// Simple insertion sort; meetings rarely have >10k segments.
	for i := 1; i < len(segs); i++ {
		for j := i; j > 0 && segs[j].StartMS < segs[j-1].StartMS; j-- {
			segs[j], segs[j-1] = segs[j-1], segs[j]
		}
	}
}

func labelForSpeaker(id string, streams []AudioStream) string {
	for _, s := range streams {
		if s.SpeakerID == id {
			return s.SpeakerLabel
		}
	}
	return id
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// CountWords counts total words across all segments.
func CountWords(segments []Segment) int {
	n := 0
	for _, s := range segments {
		n += len(s.Words)
	}
	return n
}

// DurationMsFromSegments returns end time of last segment.
func DurationMsFromSegments(segments []Segment) int64 {
	var max int64
	for _, s := range segments {
		if s.EndMS > max {
			max = s.EndMS
		}
	}
	return max
}

// ValidateSegments enforces the transcript contract before we persist or pack
// artifacts. Producer bugs must fail here instead of leaking invalid timing
// downstream.
func ValidateSegments(segments []Segment) error {
	for segIndex, seg := range segments {
		if seg.StartMS > seg.EndMS {
			return fmt.Errorf("segment %d startMs must be <= endMs (got %d > %d)", segIndex, seg.StartMS, seg.EndMS)
		}
		for wordIndex, word := range seg.Words {
			if word.StartMS > word.EndMS {
				return fmt.Errorf("segment %d word %d %q startMs must be <= endMs (got %d > %d)", segIndex, wordIndex, word.Text, word.StartMS, word.EndMS)
			}
			if word.StartMS < seg.StartMS || word.EndMS > seg.EndMS {
				return fmt.Errorf(
					"segment %d word %d %q must stay within segment bounds (segment=%d-%d word=%d-%d)",
					segIndex,
					wordIndex,
					word.Text,
					seg.StartMS,
					seg.EndMS,
					word.StartMS,
					word.EndMS,
				)
			}
		}
	}
	return nil
}

// ApplyReadableText replaces segment text with LLM-cleaned text while
// keeping original word-level timestamps.
func ApplyReadableText(original, readable []Segment) []Segment {
	out := make([]Segment, len(original))
	copy(out, original)
	for i := range out {
		if i < len(readable) {
			out[i].Text = readable[i].Text
			// Re-distribute word timings proportionally over the cleaned text.
			out[i].Words = redistributeWords(out[i].Words, readable[i].Text)
		}
	}
	return out
}

// redistributeWords keeps original timestamps but splits cleaned text
// proportionally across the original word slots.
func redistributeWords(origWords []Word, cleanedText string) []Word {
	cleanWords := strings.Fields(cleanedText)
	if len(cleanWords) == 0 || len(origWords) == 0 {
		return origWords
	}

	// Map cleaned words onto the original time slots proportionally.
	out := make([]Word, len(cleanWords))
	origN := len(origWords)
	cleanN := len(cleanWords)
	segStart := origWords[0].StartMS
	segEnd := origWords[origN-1].EndMS
	totalMS := segEnd - segStart
	if totalMS <= 0 {
		totalMS = 1
	}

	for i, w := range cleanWords {
		t0 := segStart + int64(math.Round(float64(i)*float64(totalMS)/float64(cleanN)))
		t1 := segStart + int64(math.Round(float64(i+1)*float64(totalMS)/float64(cleanN)))
		if t1 > segEnd {
			t1 = segEnd
		}
		out[i] = Word{Text: w, StartMS: t0, EndMS: t1}
	}
	return out
}
