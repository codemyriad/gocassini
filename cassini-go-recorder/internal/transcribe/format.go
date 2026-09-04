package transcribe

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gocassini/internal/meetingtime"
)

// --- transcript.words.v1.json ---

const transcriptWordsVersion = "transcript.words.v1"

type transcriptFile struct {
	Version  string                   `json:"version"`
	Media    transcriptMedia          `json:"media"`
	Speakers []speakerEntry           `json:"speakers"`
	Segments []transcriptSegmentEntry `json:"segments"`
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

type wordEntry struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	// AttributionGapDB and LowConfidenceSpeaker are omitted entirely when the
	// attribution stage did not run, so existing consumers see an unchanged
	// document and the schema stays backward compatible.
	AttributionGapDB     *float64 `json:"attributionGapDb,omitempty"`
	LowConfidenceSpeaker bool     `json:"lowConfidenceSpeaker,omitempty"`
}

// WriteTranscriptJSON writes transcript.words.v1.json.
func WriteTranscriptJSON(path string, streams []AudioStream, segments []Segment, audioDurationMS int64) error {
	if err := ValidateSegments(segments); err != nil {
		return err
	}
	return writeJSON(path, buildTranscriptFile(streams, segments, audioDurationMS, ""))
}

func buildTranscriptFile(streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string) transcriptFile {
	return transcriptFile{
		Version:  transcriptWordsVersion,
		Media:    transcriptMedia{Src: "meeting.webm", DurationMS: audioDurationMS, SHA256: sha256hex},
		Speakers: buildSpeakerEntries(streams, segments),
		Segments: buildTranscriptSegmentEntries(segments),
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
				ID:                   transcriptWordID(segmentID, wordIndex),
				Text:                 w.Text,
				StartMS:              w.StartMS,
				EndMS:                w.EndMS,
				LowConfidenceSpeaker: w.LowConfidenceSpeaker,
			}
			if w.HasAttributionGap {
				gap := w.AttributionGapDB
				words[wordIndex].AttributionGapDB = &gap
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

func transcriptSegmentID(index int) string {
	return fmt.Sprintf("seg_%06d", index)
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
	Audio       string                  `json:"audio"`
	Transcript  string                  `json:"transcript"`
	Transcripts []artifactTranscriptRef `json:"transcripts,omitempty"`
	Captions    string                  `json:"captions,omitempty"`
	Summary     string                  `json:"summary,omitempty"`
}

// artifactTranscriptRef describes an additional transcript artifact that the
// portable meeting producer embeds under files.transcripts[].
type artifactTranscriptRef struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"`
	Role       string    `json:"role"`
	Default    bool      `json:"default,omitempty"`
	Language   string    `json:"language,omitempty"`
	Provenance *provStep `json:"provenance,omitempty"`
}

type provenanceInfo struct {
	SpeechToText *provStep              `json:"speechToText,omitempty"`
	Attribution  *AttributionProvenance `json:"attribution,omitempty"`
	WordTimings  *WordTimingProvenance  `json:"wordTimings,omitempty"`
	// SourceAudio lists the speakers whose audio had their own browser capture
	// spliced over what the SFU delivered. A reader deserves to know which part
	// of the meeting came from a different recording of it, and the fit quality
	// that placed it.
	//
	// Each entry describes AUDIO. Whether the words in the transcript came from
	// that audio is a separate question, and each entry answers it separately
	// in transcript_source: the merged-mix fallback can replace the whole
	// transcript after the splice, and then no word carries any of these
	// speakers' ids at all.
	SourceAudio    []SourceRenderReport `json:"sourceAudio,omitempty"`
	MeetingSummary *provStep            `json:"meetingSummary,omitempty"`
}

// WordTimingProvenance says how this build decided where a word ends.
//
// It exists because the answer changed, and consumers cannot tell from the
// timings themselves. Builds before D-690 ended a word at its last token
// including a trailing punctuation mark, which Parakeet stamps at the *next*
// acoustic onset — so a sentence-final word could be seconds long with the
// speaker silent throughout, and consumers grew repairs that clip a suspicious
// word back towards the meeting's median. This build ends a word where the
// speaker's own audio ends, measured against the owner's track, so a long word
// is now evidence of a long sound and clipping it destroys correct timing. A
// consumer must be able to tell the two apart, and only the producer knows.
//
// Absent on every artifact built before this change, and absent on any build
// whose decoder does not declare AudioBoundedWordEnds (backend.go) — a second
// engine registered through the public registry gets no claim it did not make.
// That absence is exactly what a consumer keys off: presence means the ends
// were measured, absence means they may have been inherited from a punctuation
// mark's timestamp and the consumer's own repair should run.
type WordTimingProvenance struct {
	// EndsBoundedByAudio is true when each word's end was measured against its
	// speaker's own track rather than taken from its last token's timestamp.
	EndsBoundedByAudio bool `json:"endsBoundedByAudio"`
}

// AttributionProvenance records what the cross-track attribution stage did to
// this build's primary transcript. It exists because drop mode deletes words
// that carry their evidence away with them: without this record, the same
// recording rebuilt without CASSINI_ATTRIBUTION_DROP yields a different
// transcript with byte-identical provenance. Ran=false with a Reason also
// distinguishes a disabled or skipped stage from one that ran and measured
// nothing.
type AttributionProvenance struct {
	Ran bool `json:"ran"`
	// Mode is "annotate" (the default), "drop" (CASSINI_ATTRIBUTION_DROP) or
	// "disabled" (CASSINI_ATTRIBUTION_DISABLED).
	Mode string `json:"mode"`
	// Reason says why the stage did not run; empty when Ran is true.
	Reason        string `json:"reason,omitempty"`
	WordsMeasured int    `json:"wordsMeasured"`
	WordsFlagged  int    `json:"wordsFlagged"`
	WordsDropped  int    `json:"wordsDropped"`
	// ThresholdDB is this meeting's estimated crosstalk threshold; absent when
	// the meeting showed no crosstalk population.
	ThresholdDB *float64 `json:"thresholdDb,omitempty"`
}

type provStep struct {
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	Device  string `json:"device,omitempty"`
	// Hints is set on a speech-to-text step whose decoder was biased towards a
	// configured vocabulary. Absent means the pass ran unbiased, which is what
	// every build before this feature did.
	Hints *HintsProvenance `json:"hints,omitempty"`
}

// HintsProvenance records the decoder biasing applied to one ASR pass. It says
// what was asked for and what actually happened, because the two differ: a
// model whose bundle carries no BPE vocabulary cannot take hints at all, and a
// build that silently ignored the operator's vocabulary must not read like one
// that applied it.
type HintsProvenance struct {
	// TermCount is how many vocabulary terms were written to the hotwords file.
	// Zero with Applied false means the vocabulary could not be used.
	TermCount int `json:"termCount"`
	// Score is the per-token boost handed to sherpa-onnx.
	Score float32 `json:"score,omitempty"`
	// DecodingMethod is the search the decoder actually ran. Hotwords require
	// modified_beam_search; an unbiased pass stays on greedy_search.
	DecodingMethod string `json:"decodingMethod,omitempty"`
	// Applied is false when a vocabulary was configured but could not be
	// applied. Reason then says why, in words an operator can act on.
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
}

// WriteManifest writes manifest.json summarising the build. srcDurationMS is
// source-container provenance, while digestDurationMS describes the final
// playable meeting.webm. additional carries extra transcript files produced by
// secondary STT models; each becomes a files.transcripts[] entry alongside the
// primary transcript.words.v1.json. sttBackend is the already-resolved
// recognizer id and sttDevice the already-resolved device used by both the
// primary and additional ASR passes; provenance must name the engine that
// actually produced the words, not assume the bundled one. attribution is the
// attribution stage's record for the primary transcript (nil omits it).
//
// wordTimings is the word-end guarantee this build earned, and nil omits it.
// It is a parameter rather than a constant because the producer cannot know
// the answer on its own: it depends on which decoder ran (see
// AudioBoundedWordEnds in backend.go), and a manifest that asserted it here
// would claim it for every future backend too.
// ManifestInput carries everything WriteManifest needs to describe one build.
// It is a struct rather than a parameter list because the list had grown past
// the point where a reader could tell the two device strings and the three
// booleans apart at a call site.
type ManifestInput struct {
	SrcBasename      string
	SrcDurationMS    int64
	DigestDurationMS int64
	Streams          []AudioStream
	Segments         []Segment

	STTBackend string
	STTModelID ModelID
	STTDevice  string
	// Hints records the decoder biasing applied to this build, or nil when the
	// build ran without a vocabulary.
	Hints *HintsProvenance

	SummaryModel string
	HasSummary   bool

	Additional  []AdditionalTranscript
	Attribution *AttributionProvenance
	WordTimings *WordTimingProvenance
	// SourceAudio is one report per speaker whose transcription input had that
	// participant's own browser capture spliced over the recorded track. Empty
	// for every build with no uploads, which omits the key entirely.
	SourceAudio []SourceRenderReport
}

func WriteManifest(path string, in ManifestInput) error {
	wordCount := 0
	for _, seg := range in.Segments {
		wordCount += len(seg.Words)
	}

	files := artifactFiles{
		Audio:      "meeting.webm",
		Transcript: "transcript.words.v1.json",
		Captions:   "captions.vtt",
	}
	if len(in.Additional) > 0 {
		primaryID := sanitizeTranscriptID(string(in.STTModelID))
		files.Transcripts = append(files.Transcripts, artifactTranscriptRef{
			ID: primaryID, Path: "transcript.words.v1.json", Role: "raw-asr", Default: true,
			Provenance: &provStep{Backend: in.STTBackend, Model: string(in.STTModelID), Device: in.STTDevice, Hints: in.Hints},
		})
		for _, extra := range in.Additional {
			extraBackend := extra.Backend
			if extraBackend == "" {
				// Additional passes run on the primary pass's backend unless
				// the producer recorded otherwise.
				extraBackend = in.STTBackend
			}
			files.Transcripts = append(files.Transcripts, artifactTranscriptRef{
				ID: extra.ID, Path: extra.Path, Role: "raw-asr",
				Provenance: &provStep{Backend: extraBackend, Model: string(extra.ModelID), Device: in.STTDevice},
			})
		}
	}
	if in.HasSummary {
		files.Summary = "summary.md"
	}

	prov := &provenanceInfo{
		SpeechToText: &provStep{
			Backend: in.STTBackend,
			Model:   string(in.STTModelID),
			Device:  in.STTDevice,
			Hints:   in.Hints,
		},
		Attribution: in.Attribution,
		// nil here writes no wordTimings key at all, which is what a consumer
		// reads as "these ends were not measured, run the legacy repair". A
		// caller that cannot prove the guarantee must pass nil rather than a
		// false record: absence is the honest answer, and it is the one shape
		// every existing consumer already handles.
		WordTimings: in.WordTimings,
		SourceAudio: in.SourceAudio,
	}
	if in.HasSummary {
		prov.MeetingSummary = &provStep{
			Backend: "openai-compatible",
			Model:   in.SummaryModel,
		}
	}

	doc := artifactManifest{
		Kind:        "cassini.meeting-artifact.v1",
		Version:     "1",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Source: artifactSource{
			Basename:        in.SrcBasename,
			DurationMS:      in.SrcDurationMS,
			RecordedAtLocal: meetingtime.InferRecordedAtLocal(in.SrcBasename),
		},
		Files:            files,
		SpeakerCount:     logicalSpeakerCount(in.Streams),
		SegmentCount:     len(in.Segments),
		DigestDurationMS: in.DigestDurationMS,
		WordCount:        wordCount,
		Provenance:       prov,
	}
	return writeJSON(path, doc)
}

func logicalSpeakerCount(streams []AudioStream) int {
	seen := make(map[string]struct{}, len(streams))
	for _, stream := range streams {
		// Negative stream indexes are synthetic transcript sources (currently
		// the mixed-track fallback), not meeting participants.
		if stream.Index < 0 {
			continue
		}
		id := strings.TrimSpace(stream.SpeakerID)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	return len(seen)
}

// --- segment assembly ---

const (
	defaultSegmentGapThresholdMS int64 = 1500
	defaultSegmentMaxWords             = 60
)

// AssembleSegments groups words from a speaker into logical segments.
// A new segment starts when there is a gap > gapThresholdMS or the
// segment exceeds maxWords.
func AssembleSegments(speakerID string, words []Word, gapThresholdMS int64, maxWords int) []Segment {
	if gapThresholdMS <= 0 {
		gapThresholdMS = defaultSegmentGapThresholdMS
	}
	if maxWords <= 0 {
		maxWords = defaultSegmentMaxWords
	}

	var segments []Segment
	var cur []Word
	var curStartMS, curEndMS int64

	flush := func() {
		if len(cur) == 0 {
			return
		}
		text := wordsToText(cur)
		segments = append(segments, Segment{
			SpeakerID: speakerID,
			StartMS:   curStartMS,
			EndMS:     curEndMS,
			Text:      text,
			Words:     append([]Word(nil), cur...),
		})
		cur = cur[:0]
		curStartMS = 0
		curEndMS = 0
	}

	for _, w := range words {
		if len(cur) > 0 {
			gap := w.StartMS - curEndMS
			if gap > gapThresholdMS || len(cur) >= maxWords {
				flush()
			}
		}
		if len(cur) == 0 {
			curStartMS = w.StartMS
			curEndMS = w.EndMS
		} else {
			if w.StartMS < curStartMS {
				curStartMS = w.StartMS
			}
			if w.EndMS > curEndMS {
				curEndMS = w.EndMS
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

// MergeAndSortSegments merges per-speaker segment lists in word-time order.
//
// Sorting already-assembled segments as opaque blocks loses short interjections:
// a speaker's long segment can span another speaker's complete comment, causing
// that comment to be rendered only after the long segment. Rebuilding segments
// from the canonical word timestamps makes each speaker change a turn boundary,
// while retaining the same default gap and word-count limits as AssembleSegments.
func MergeAndSortSegments(perSpeaker [][]Segment) []Segment {
	type attributedWord struct {
		speakerID string
		word      Word
	}

	var words []attributedWord
	var wordless []Segment
	for _, segs := range perSpeaker {
		for _, seg := range segs {
			if len(seg.Words) == 0 {
				// Text-only segments predate the word-level transcript contract.
				// Keep them as opaque records so merging never drops caller data.
				wordless = append(wordless, seg)
				continue
			}
			for _, word := range seg.Words {
				words = append(words, attributedWord{speakerID: seg.SpeakerID, word: word})
			}
		}
	}

	// Stable ordering makes equal-start words deterministic: they retain the
	// caller's per-speaker/segment/word order.
	sort.SliceStable(words, func(i, j int) bool {
		return words[i].word.StartMS < words[j].word.StartMS
	})

	var merged []Segment
	var currentSpeaker string
	var currentWords []Word
	var currentStartMS, currentEndMS int64
	flush := func() {
		if len(currentWords) == 0 {
			return
		}
		merged = append(merged, Segment{
			SpeakerID: currentSpeaker,
			StartMS:   currentStartMS,
			EndMS:     currentEndMS,
			Text:      wordsToText(currentWords),
			Words:     append([]Word(nil), currentWords...),
		})
		currentWords = currentWords[:0]
		currentStartMS = 0
		currentEndMS = 0
	}

	for _, attributed := range words {
		if len(currentWords) > 0 {
			gap := attributed.word.StartMS - currentEndMS
			if attributed.speakerID != currentSpeaker ||
				gap > defaultSegmentGapThresholdMS ||
				len(currentWords) >= defaultSegmentMaxWords {
				flush()
			}
		}
		if len(currentWords) == 0 {
			currentSpeaker = attributed.speakerID
			currentStartMS = attributed.word.StartMS
			currentEndMS = attributed.word.EndMS
		} else {
			if attributed.word.StartMS < currentStartMS {
				currentStartMS = attributed.word.StartMS
			}
			if attributed.word.EndMS > currentEndMS {
				currentEndMS = attributed.word.EndMS
			}
		}
		currentWords = append(currentWords, attributed.word)
	}
	flush()

	merged = append(merged, wordless...)
	sortSegments(merged)
	return merged
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
	return writeJSONWithSync(path, v, (*os.File).Sync)
}

// writeJSONWithSync writes JSON through a same-directory temporary file so a
// concurrent reader sees either the old document or the complete new one. The
// sync hook exists to exercise the pre-rename durability failure path in tests.
func writeJSONWithSync(path string, v any, syncFile func(*os.File) error) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	perm := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
		// os.WriteFile used to retain an existing file's mode. Use it as the
		// creation ceiling so an atomic replacement never widens permissions;
		// OpenFile may still narrow it according to the current umask.
		perm = info.Mode().Perm()
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	tmp, err := createJSONTemp(dir, filepath.Base(path), perm)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// createJSONTemp is equivalent to CreateTemp with a caller-specified creation
// mode. OpenFile applies the process umask when the file is created, preserving
// the old WriteFile creation semantics (and any stricter existing-mode ceiling)
// while O_EXCL keeps the random name safe from clobbering or symlink races.
func createJSONTemp(dir, base string, perm os.FileMode) (*os.File, error) {
	var suffix [16]byte
	for range 100 {
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, fmt.Errorf("generate temporary JSON filename: %w", err)
		}
		tmpPath := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%x", base, suffix))
		tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return tmp, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("create temporary JSON file for %s: too many collisions", base)
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
