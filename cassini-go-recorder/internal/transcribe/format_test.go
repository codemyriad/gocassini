package transcribe

import (
	"encoding/json"
	"errors"
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

	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, segments, ModelID("test-stt"), "cuda", "test-llm", true, "summary-model", true, nil); err != nil {
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

	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, nil, ModelID("test-stt"), "cuda", "", false, "", false, nil); err != nil {
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

	if err := WriteManifest(path, "src.mkv", 1000, 1000, streams, segments, ModelID("test-stt"), "cuda", "", false, "", false, nil); err != nil {
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
