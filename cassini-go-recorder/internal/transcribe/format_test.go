package transcribe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJSONSyncFailureLeavesExistingDocumentIntact(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.json")
	oldDocument := []byte("old document\n")
	if err := os.WriteFile(path, oldDocument, 0o600); err != nil {
		t.Fatalf("seed old document: %v", err)
	}

	syncErr := errors.New("forced sync failure")
	syncCalled := false
	err := writeJSONWithSync(path, map[string]string{"new": "document"}, func(file *os.File) error {
		syncCalled = true
		info, statErr := file.Stat()
		if statErr != nil {
			t.Fatalf("stat temporary file before sync: %v", statErr)
		}
		if info.Size() == 0 {
			t.Fatal("sync called before JSON was written")
		}
		return syncErr
	})
	if !errors.Is(err, syncErr) {
		t.Fatalf("writeJSONWithSync error = %v, want %v", err, syncErr)
	}
	if !syncCalled {
		t.Fatal("writeJSONWithSync did not sync the temporary file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved document: %v", err)
	}
	if string(got) != string(oldDocument) {
		t.Fatalf("destination changed before successful sync: got %q, want %q", got, oldDocument)
	}
	matches, err := filepath.Glob(filepath.Join(tmp, ".transcript.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind after sync failure: %v", matches)
	}
}

func TestValidateSegmentsRejectsReversedWordRanges(t *testing.T) {
	err := ValidateSegments([]Segment{
		{
			SpeakerID: "spk_1",
			StartMS:   1000,
			EndMS:     1500,
			Text:      "hello there",
			Words: []Word{
				{Text: "hello", StartMS: 1000, EndMS: 1200},
				{Text: "there", StartMS: 1300, EndMS: 1299},
			},
		},
	})
	if err == nil {
		t.Fatal("expected ValidateSegments to reject reversed word timing")
	}
	if !strings.Contains(err.Error(), `word 1 "there" startMs must be <= endMs`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSegmentsRejectsOutOfBoundsWordRanges(t *testing.T) {
	err := ValidateSegments([]Segment{
		{
			SpeakerID: "spk_1",
			StartMS:   1000,
			EndMS:     1500,
			Text:      "hello",
			Words: []Word{
				{Text: "hello", StartMS: 900, EndMS: 1200},
			},
		},
	})
	if err == nil {
		t.Fatal("expected ValidateSegments to reject out-of-bounds word timing")
	}
	if !strings.Contains(err.Error(), `must stay within segment bounds`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSegmentAssemblyUsesFullOverlappingWordEnvelope(t *testing.T) {
	words := []Word{
		{Text: "funnels.", StartMS: 890199, EndMS: 890760},
		{Text: "following", StartMS: 890300, EndMS: 890739},
	}

	builders := map[string]func() []Segment{
		"assemble": func() []Segment {
			return AssembleSegments("spk_a", words, defaultSegmentGapThresholdMS, defaultSegmentMaxWords)
		},
		"merge": func() []Segment {
			return MergeAndSortSegments([][]Segment{{{
				SpeakerID: "spk_a",
				Words:     words,
			}}})
		},
	}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			got := build()
			if len(got) != 1 {
				t.Fatalf("segments = %d, want 1: %#v", len(got), got)
			}
			if got[0].StartMS != 890199 || got[0].EndMS != 890760 {
				t.Fatalf("segment bounds = %d-%d, want 890199-890760", got[0].StartMS, got[0].EndMS)
			}
			if err := ValidateSegments(got); err != nil {
				t.Fatalf("ValidateSegments() error = %v", err)
			}
		})
	}
}

func TestSegmentAssemblyGapUsesRunningMaximumEnd(t *testing.T) {
	words := []Word{
		{Text: "long", StartMS: 0, EndMS: 5000},
		{Text: "nested", StartMS: 100, EndMS: 200},
		{Text: "still-overlapping", StartMS: 1701, EndMS: 1800},
	}

	builders := map[string]func() []Segment{
		"assemble": func() []Segment {
			return AssembleSegments("spk_a", words, defaultSegmentGapThresholdMS, defaultSegmentMaxWords)
		},
		"merge": func() []Segment {
			return MergeAndSortSegments([][]Segment{{{
				SpeakerID: "spk_a",
				Words:     words,
			}}})
		},
	}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			got := build()
			if len(got) != 1 {
				t.Fatalf("running maximum end should keep overlapping words together; got %d segments: %#v", len(got), got)
			}
			if got[0].StartMS != 0 || got[0].EndMS != 5000 {
				t.Fatalf("segment bounds = %d-%d, want 0-5000", got[0].StartMS, got[0].EndMS)
			}
		})
	}
}

func TestMergeAndSortSegmentsSplitsAroundInterjection(t *testing.T) {
	perSpeaker := [][]Segment{
		{
			{
				SpeakerID: "spk_a",
				StartMS:   1000,
				EndMS:     2900,
				Text:      "before continuing after",
				Words: []Word{
					{Text: "before", StartMS: 1000, EndMS: 1300},
					{Text: "continuing", StartMS: 1800, EndMS: 2200},
					{Text: "after", StartMS: 2600, EndMS: 2900},
				},
			},
		},
		{
			{
				SpeakerID: "spk_b",
				StartMS:   2300,
				EndMS:     2500,
				Text:      "yes",
				Words:     []Word{{Text: "yes", StartMS: 2300, EndMS: 2500}},
			},
		},
	}

	got := MergeAndSortSegments(perSpeaker)
	if len(got) != 3 {
		t.Fatalf("expected A/B/A turns, got %d segments: %#v", len(got), got)
	}

	wantSpeakers := []string{"spk_a", "spk_b", "spk_a"}
	wantTexts := []string{"before continuing", "yes", "after"}
	wantBounds := [][2]int64{{1000, 2200}, {2300, 2500}, {2600, 2900}}
	for i := range got {
		if got[i].SpeakerID != wantSpeakers[i] {
			t.Errorf("segment %d speaker = %q, want %q", i, got[i].SpeakerID, wantSpeakers[i])
		}
		if got[i].Text != wantTexts[i] {
			t.Errorf("segment %d text = %q, want %q", i, got[i].Text, wantTexts[i])
		}
		if got[i].StartMS != wantBounds[i][0] || got[i].EndMS != wantBounds[i][1] {
			t.Errorf("segment %d bounds = %d-%d, want %d-%d", i, got[i].StartMS, got[i].EndMS, wantBounds[i][0], wantBounds[i][1])
		}
	}
	if gotWords := CountWords(got); gotWords != 4 {
		t.Fatalf("merged word count = %d, want 4", gotWords)
	}
}

func TestMergeAndSortSegmentsRetainsStableEqualTimeOrder(t *testing.T) {
	got := MergeAndSortSegments([][]Segment{
		{{SpeakerID: "spk_a", Words: []Word{{Text: "a", StartMS: 100, EndMS: 200}}}},
		{{SpeakerID: "spk_b", Words: []Word{{Text: "b", StartMS: 100, EndMS: 150}}}},
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 segments, got %d: %#v", len(got), got)
	}
	if got[0].SpeakerID != "spk_a" || got[1].SpeakerID != "spk_b" {
		t.Fatalf("equal-time order = %q, %q; want stable spk_a, spk_b", got[0].SpeakerID, got[1].SpeakerID)
	}
}

func TestMergeAndSortSegmentsRetainsDefaultGapAndWordLimits(t *testing.T) {
	words := make([]Word, 0, defaultSegmentMaxWords+2)
	for i := 0; i <= defaultSegmentMaxWords; i++ {
		start := int64(i * 100)
		words = append(words, Word{Text: "word", StartMS: start, EndMS: start + 50})
	}
	lastEnd := words[len(words)-1].EndMS
	words = append(words, Word{
		Text:    "after-gap",
		StartMS: lastEnd + defaultSegmentGapThresholdMS + 1,
		EndMS:   lastEnd + defaultSegmentGapThresholdMS + 101,
	})

	got := MergeAndSortSegments([][]Segment{{{
		SpeakerID: "spk_a",
		Words:     words,
	}}})

	if len(got) != 3 {
		t.Fatalf("expected max-word and gap splits, got %d segments", len(got))
	}
	if len(got[0].Words) != defaultSegmentMaxWords || len(got[1].Words) != 1 || len(got[2].Words) != 1 {
		t.Fatalf("segment word counts = %d, %d, %d; want %d, 1, 1", len(got[0].Words), len(got[1].Words), len(got[2].Words), defaultSegmentMaxWords)
	}
	if CountWords(got) != len(words) {
		t.Fatalf("merged word count = %d, want %d", CountWords(got), len(words))
	}
}

func TestMergeAndSortSegmentsPreservesWordlessSegments(t *testing.T) {
	legacy := Segment{SpeakerID: "legacy", StartMS: 500, EndMS: 700, Text: "legacy text"}
	got := MergeAndSortSegments([][]Segment{
		{legacy},
		{{SpeakerID: "spk_a", Words: []Word{{Text: "hello", StartMS: 1000, EndMS: 1200}}}},
	})

	if len(got) != 2 {
		t.Fatalf("expected both legacy and timed segments, got %d: %#v", len(got), got)
	}
	if got[0].SpeakerID != legacy.SpeakerID || got[0].StartMS != legacy.StartMS ||
		got[0].EndMS != legacy.EndMS || got[0].Text != legacy.Text || len(got[0].Words) != 0 {
		t.Fatalf("wordless segment changed or was reordered: %#v", got[0])
	}
}

func TestWriteManifestRecordsSummaryWhenPresent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{{SpeakerID: "spk_alex", StartMS: 0, EndMS: 1000, Text: "hi", Words: []Word{{Text: "hi", StartMS: 0, EndMS: 1000}}}}

	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, segments, SherpaOnnxBackend, ModelID("test-stt"), "cuda", "test-llm", true, "summary-model", true, nil, nil); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	var got artifactManifest
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if got.Files.Summary != "summary.md" {
		t.Errorf("files.summary = %q, want %q", got.Files.Summary, "summary.md")
	}
	if got.Provenance == nil || got.Provenance.MeetingSummary == nil {
		t.Fatal("provenance.meetingSummary missing")
	}
	if got.Provenance.MeetingSummary.Model != "summary-model" {
		t.Errorf("meetingSummary.model = %q, want %q", got.Provenance.MeetingSummary.Model, "summary-model")
	}
	if got.Provenance.MeetingSummary.Backend != "openai-compatible" {
		t.Errorf("meetingSummary.backend = %q, want %q", got.Provenance.MeetingSummary.Backend, "openai-compatible")
	}
}

func TestWriteManifestCountsUniqueLogicalSpeakers(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{
		{Index: 0, ParticipantID: "alice", SpeakerID: "spk_alice", SpeakerLabel: "Alice"},
		{Index: 1, ParticipantID: "alice", SpeakerID: "spk_alice", SpeakerLabel: "Alice"}, // rejoin stream
		{Index: 2, ParticipantID: "bob", SpeakerID: "spk_bob", SpeakerLabel: "Bob"},
		{Index: -1, SpeakerID: "merged", SpeakerLabel: "Everyone"}, // synthetic fallback, not a participant
	}

	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, nil, SherpaOnnxBackend, ModelID("test-stt"), "cuda", "", false, "", false, nil, nil); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	var got artifactManifest
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if got.SpeakerCount != 2 {
		t.Fatalf("speakerCount = %d, want 2 unique logical speakers", got.SpeakerCount)
	}
}

func TestWriteTranscriptJSONEmitsEmptySpeakersArrayForSilentRecording(t *testing.T) {
	tmp := t.TempDir()
	wordsPath := filepath.Join(tmp, "transcript.words.v1.json")
	readablePath := filepath.Join(tmp, "transcript.readable.v1.json")

	if err := WriteTranscriptJSON(wordsPath, nil, nil, 45049); err != nil {
		t.Fatalf("WriteTranscriptJSON: %v", err)
	}
	if err := WriteReadableTranscriptJSON(readablePath, nil, nil, 45049); err != nil {
		t.Fatalf("WriteReadableTranscriptJSON: %v", err)
	}

	for _, path := range []string{wordsPath, readablePath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(raw), `"speakers": []`) {
			t.Errorf("%s must serialize speakers as [] for silent recordings (viewer validator rejects null), got:\n%s", filepath.Base(path), raw)
		}
	}
}

func TestWriteManifestOmitsSummaryWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{{SpeakerID: "spk_alex", StartMS: 0, EndMS: 1000, Text: "hi", Words: []Word{{Text: "hi", StartMS: 0, EndMS: 1000}}}}

	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, segments, SherpaOnnxBackend, ModelID("test-stt"), "cuda", "", false, "", false, nil, nil); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(raw), `"summary"`) {
		t.Errorf("manifest should not mention summary when absent, got %s", string(raw))
	}
	if strings.Contains(string(raw), `"meetingSummary"`) {
		t.Errorf("manifest should not mention meetingSummary when absent, got %s", string(raw))
	}
}

// One flagged source word must flag exactly one cleaned word even when
// cleanup shortens the text — the ordinary case. The cleaned→source argmax
// alone loses the flag there: with fewer slots than source words every slot
// is wider than a source word, the flagged word straddles two slots and is
// the argmax of neither, and the summary silently reads the crosstalk word.
func TestReadableCleanupShrinkNeverLosesTheFlag(t *testing.T) {
	cases := []struct{ orig, clean, flagged int }{
		{10, 9, 5}, {8, 7, 4}, {5, 4, 2}, {3, 2, 1}, {10, 7, 5}, {4, 2, 3},
	}
	for _, tc := range cases {
		words := make([]Word, tc.orig)
		for i := range words {
			words[i] = Word{Text: fmt.Sprintf("w%d", i), StartMS: int64(i * 100), EndMS: int64((i + 1) * 100)}
		}
		words[tc.flagged].LowConfidenceSpeaker = true
		words[tc.flagged].HasAttributionGap = true
		words[tc.flagged].AttributionGapDB = 21.5

		cleanTexts := make([]string, tc.clean)
		for i := range cleanTexts {
			cleanTexts[i] = fmt.Sprintf("c%d", i)
		}
		original := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: int64(tc.orig * 100),
			Text: "orig", Words: words}}
		readable := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: int64(tc.orig * 100),
			Text: strings.Join(cleanTexts, " ")}}

		applied := ApplyReadableText(original, readable)
		var flagged int
		var gap float64
		var hasGap bool
		for _, w := range applied[0].Words {
			if w.LowConfidenceSpeaker {
				flagged++
				gap = w.AttributionGapDB
				hasGap = w.HasAttributionGap
			}
		}
		if flagged != 1 {
			t.Errorf("%d->%d words (flagged source %d): got %d flagged cleaned words, want exactly 1",
				tc.orig, tc.clean, tc.flagged, flagged)
			continue
		}
		if !hasGap || gap != 21.5 {
			t.Errorf("%d->%d words: the flagged cleaned word must carry the source gap, got has=%v gap=%.1f",
				tc.orig, tc.clean, hasGap, gap)
		}
	}
}

// When two contradicted source words collapse into one cleaned word, that
// word is flagged once and carries the largest measured gap among the flagged
// contributors — never a smaller gap and never a duplicate flag.
func TestReadableCleanupCollapsedFlagsCarryTheMaxGap(t *testing.T) {
	original := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 400, Text: "a b c d",
		Words: []Word{
			{Text: "a", StartMS: 0, EndMS: 100},
			{Text: "b", StartMS: 100, EndMS: 200},
			{Text: "c", StartMS: 200, EndMS: 300,
				LowConfidenceSpeaker: true, HasAttributionGap: true, AttributionGapDB: 12.0},
			{Text: "d", StartMS: 300, EndMS: 400,
				LowConfidenceSpeaker: true, HasAttributionGap: true, AttributionGapDB: 30.5},
		}}}
	readable := []Segment{{SpeakerID: "spk_a", StartMS: 0, EndMS: 400, Text: "a merged"}}

	applied := ApplyReadableText(original, readable)
	var flagged int
	var gap float64
	for _, w := range applied[0].Words {
		if w.LowConfidenceSpeaker {
			flagged++
			gap = w.AttributionGapDB
		}
	}
	if flagged != 1 {
		t.Fatalf("two collapsed flagged sources must flag one cleaned word, got %d", flagged)
	}
	if gap != 30.5 {
		t.Errorf("the flagged word must carry the max contributing gap, got %.1f", gap)
	}
}

// Drop mode deletes words that carry their evidence away with them; the
// manifest record is the only remaining trace of the deletion. It must carry
// the mode, the counts and the threshold — and stay entirely absent when the
// producer recorded nothing, so older documents are byte-identical.
func TestWriteManifestRecordsAttributionProvenance(t *testing.T) {
	tmp := t.TempDir()
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	threshold := 14.5
	attr := &AttributionProvenance{
		Ran:           true,
		Mode:          "drop",
		WordsMeasured: 120,
		WordsFlagged:  3,
		WordsDropped:  3,
		ThresholdDB:   &threshold,
	}

	path := filepath.Join(tmp, "manifest.json")
	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, nil, SherpaOnnxBackend, ModelID("test-stt"), "cpu", "", false, "", false, nil, attr); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got struct {
		Provenance struct {
			Attribution map[string]any `json:"attribution"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	a := got.Provenance.Attribution
	if a == nil {
		t.Fatal("provenance.attribution missing")
	}
	if a["ran"] != true {
		t.Errorf("ran = %v, want true", a["ran"])
	}
	if a["mode"] != "drop" {
		t.Errorf("mode = %v, want drop", a["mode"])
	}
	if a["wordsMeasured"] != float64(120) {
		t.Errorf("wordsMeasured = %v, want 120", a["wordsMeasured"])
	}
	if a["wordsFlagged"] != float64(3) {
		t.Errorf("wordsFlagged = %v, want 3", a["wordsFlagged"])
	}
	if a["wordsDropped"] != float64(3) {
		t.Errorf("wordsDropped = %v, want 3", a["wordsDropped"])
	}
	if a["thresholdDb"] != 14.5 {
		t.Errorf("thresholdDb = %v, want 14.5", a["thresholdDb"])
	}

	// Absent entirely for a producer that records nothing (legacy callers).
	legacyPath := filepath.Join(tmp, "manifest-legacy.json")
	if err := WriteManifest(legacyPath, "src.mkv", 1000, 1000, streams, nil, SherpaOnnxBackend, ModelID("test-stt"), "cpu", "", false, "", false, nil, nil); err != nil {
		t.Fatalf("WriteManifest legacy: %v", err)
	}
	legacyRaw, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy manifest: %v", err)
	}
	if strings.Contains(string(legacyRaw), `"attribution"`) {
		t.Errorf("attribution must be omitted when not recorded, got:\n%s", legacyRaw)
	}
}

// Every build this package produces measures each word's end against its own
// speaker's track. Consumers cannot see that from the timings — a long word
// looks the same whether it was measured or inherited from a punctuation
// mark's timestamp — and the 197 artifacts built before the rule changed carry
// timings of the second kind, which consumers repair by clipping. So the claim
// has to be stated in the manifest, and stated unconditionally: there is no
// build of this code that does not make it.
func TestWriteManifestAlwaysRecordsAudioBoundedWordEnds(t *testing.T) {
	tmp := t.TempDir()
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}

	// The leanest possible call: no readable pass, no summary, no additional
	// transcripts, no attribution record. The marker must still be there.
	path := filepath.Join(tmp, "manifest.json")
	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, nil, SherpaOnnxBackend, ModelID("test-stt"), "cpu", "", false, "", false, nil, nil); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got struct {
		Provenance struct {
			WordTimings map[string]any `json:"wordTimings"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	timings := got.Provenance.WordTimings
	if timings == nil {
		t.Fatalf("provenance.wordTimings missing from:\n%s", raw)
	}
	if timings["endsBoundedByAudio"] != true {
		t.Errorf("endsBoundedByAudio = %v, want true", timings["endsBoundedByAudio"])
	}
	// Exactly one key: consumers key off the record's presence, and every key
	// added here has to be declared by the closed portable-meeting schemas
	// before it can be published.
	if len(timings) != 1 {
		t.Errorf("wordTimings = %v; want exactly one key, endsBoundedByAudio", timings)
	}
}
