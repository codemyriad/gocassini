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

	if err := WriteManifest(path, ManifestInput{SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000, Streams: streams, Segments: segments, STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cuda", SummaryModel: "summary-model", HasSummary: true}); err != nil {
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

	if err := WriteManifest(path, ManifestInput{SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000, Streams: streams, STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cuda"}); err != nil {
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

	if err := WriteTranscriptJSON(wordsPath, nil, nil, 45049); err != nil {
		t.Fatalf("WriteTranscriptJSON: %v", err)
	}

	for _, path := range []string{wordsPath} {
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

	if err := WriteManifest(path, ManifestInput{SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000, Streams: streams, Segments: segments, STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cuda"}); err != nil {
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
	if err := WriteManifest(path, ManifestInput{SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000, Streams: streams, STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cpu", Attribution: attr}); err != nil {
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
	if err := WriteManifest(legacyPath, ManifestInput{SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000, Streams: streams, STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cpu"}); err != nil {
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

// provenance.wordTimings is a claim about how the words were timed, so
// WriteManifest writes exactly what the caller earned and nothing more.
// Presence with endsBoundedByAudio:true means the ends were measured against
// the speaker's own audio; absence of the whole object means they may have
// been inherited from a punctuation mark's timestamp, and a consumer keys off
// that absence to run its own repair. Both halves are pinned here, because
// writing the record by default is precisely the bug this replaced: it would
// hand the guarantee to any backend that never made it.
func TestWriteManifestWritesWordTimingsOnlyWhenTheCallerEarnedIt(t *testing.T) {
	tmp := t.TempDir()
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}

	readWordTimings := func(t *testing.T, path string) (map[string]any, string) {
		t.Helper()
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
		return got.Provenance.WordTimings, string(raw)
	}

	// The leanest possible call: no summary, no additional transcripts, no
	// attribution record. The earned marker must still be there.
	measured := filepath.Join(tmp, "manifest.json")
	if err := WriteManifest(measured, ManifestInput{
		SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000,
		Streams: streams, STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cpu",
		WordTimings: &WordTimingProvenance{EndsBoundedByAudio: true},
	}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	timings, raw := readWordTimings(t, measured)
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

	// A caller that did not earn it passes nil, and the key must not exist at
	// all — not endsBoundedByAudio:false, which a consumer reading the object
	// rather than the flag could still misread as a producer that measured.
	unmeasured := filepath.Join(tmp, "manifest-unmeasured.json")
	if err := WriteManifest(unmeasured, ManifestInput{SrcBasename: "src.mkv", SrcDurationMS: 1000, DigestDurationMS: 1000, Streams: streams, STTBackend: "some-other-engine", STTModelID: ModelID("test-stt"), STTDevice: "cpu"}); err != nil {
		t.Fatalf("WriteManifest (unmeasured): %v", err)
	}
	absent, rawAbsent := readWordTimings(t, unmeasured)
	if absent != nil {
		t.Errorf("wordTimings = %v, want the key absent entirely", absent)
	}
	if strings.Contains(rawAbsent, "wordTimings") {
		t.Errorf("the manifest still mentions wordTimings:\n%s", rawAbsent)
	}
	// The rest of the provenance object must survive, or this half of the test
	// would pass on a manifest that lost provenance altogether.
	if !strings.Contains(rawAbsent, `"speechToText"`) {
		t.Errorf("provenance.speechToText went missing:\n%s", rawAbsent)
	}
}

func TestWriteManifestRecordsSourceAudioProvenance(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{
		{Index: 0, ParticipantID: "alice", SpeakerID: "spk_alice", SpeakerLabel: "Alice"},
		{Index: 1, ParticipantID: "bob", SpeakerID: "spk_bob", SpeakerLabel: "Bob"},
	}
	reports := []SourceRenderReport{
		{SpeakerID: "spk_alice", Owner: "alice", Segments: 3, Placed: 3, Anchors: 24, SplicedMS: 24000},
		{SpeakerID: "spk_bob", Owner: "bob", Segments: 1, Placed: 1, Anchors: 8, SplicedMS: 8000},
	}
	if err := WriteManifest(path, ManifestInput{
		SrcBasename: "src.mkv", SrcDurationMS: 30000, DigestDurationMS: 30000,
		Streams:     streams,
		STTBackend:  SherpaOnnxBackend,
		STTModelID:  ModelID("test-stt"),
		STTDevice:   "cpu",
		SourceAudio: reports,
	}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var doc struct {
		Provenance struct {
			SourceAudio []SourceRenderReport `json:"sourceAudio"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(doc.Provenance.SourceAudio) != 2 {
		t.Fatalf("sourceAudio = %d entries, want 2: %s", len(doc.Provenance.SourceAudio), raw)
	}
	if doc.Provenance.SourceAudio[0].Owner != "alice" || doc.Provenance.SourceAudio[0].Placed != 3 {
		t.Errorf("alice report = %+v, want owner=alice placed=3", doc.Provenance.SourceAudio[0])
	}
	if doc.Provenance.SourceAudio[1].Owner != "bob" || doc.Provenance.SourceAudio[1].Placed != 1 {
		t.Errorf("bob report = %+v, want owner=bob placed=1", doc.Provenance.SourceAudio[1])
	}
}

// The manifest has to say whether the published audio carries the splice, and
// where. Somebody looking at a meeting should be able to tell which stretches of
// what they are hearing came from whose upload without listening for it.
func TestWriteManifestRecordsWhetherTheMixWasSpliced(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{
		{Index: 0, ParticipantID: "alice", SpeakerID: "spk_alice", SpeakerLabel: "Alice"},
		{Index: 1, ParticipantID: "bob", SpeakerID: "spk_bob", SpeakerLabel: "Bob"},
	}
	reports := []SourceRenderReport{
		{
			SpeakerID: "spk_alice", Owner: "alice", Segments: 2, Placed: 2, SplicedMS: 24000,
			MixSpliced: true, CrossfadeMS: mixSpliceCrossfadeMS, RenderHz: mixRenderHz,
			Windows: []SpliceWindow{{FromMS: 1000, ToMS: 13000, Segment: 0}, {FromMS: 20000, ToMS: 32000, Segment: 1}},
		},
		{
			SpeakerID: "spk_bob", Owner: "bob", Segments: 1, Placed: 1, SplicedMS: 8000,
			MixSkipReason: "disabled by configuration (CASSINI_SOURCE_AUDIO_MIX=0)", RenderHz: mixRenderHz,
		},
	}
	if err := WriteManifest(path, ManifestInput{
		SrcBasename: "src.mkv", SrcDurationMS: 40000, DigestDurationMS: 40000, Streams: streams,
		STTBackend: SherpaOnnxBackend, STTModelID: ModelID("test-stt"), STTDevice: "cpu",
		SourceAudio: reports,
	}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var doc struct {
		Provenance struct {
			SourceAudio []SourceRenderReport `json:"sourceAudio"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	alice := doc.Provenance.SourceAudio[0]
	if !alice.MixSpliced {
		t.Fatalf("alice's report does not record that the mix was spliced: %+v", alice)
	}
	if alice.CrossfadeMS != mixSpliceCrossfadeMS || alice.RenderHz != mixRenderHz {
		t.Fatalf("alice's render is recorded as %d Hz with a %d ms crossfade", alice.RenderHz, alice.CrossfadeMS)
	}
	if len(alice.Windows) != 2 || alice.Windows[1].FromMS != 20000 || alice.Windows[1].Segment != 1 {
		t.Fatalf("alice's windows did not survive the manifest: %+v", alice.Windows)
	}
	bob := doc.Provenance.SourceAudio[1]
	if bob.MixSpliced {
		t.Fatal("bob's report claims a mix splice that did not happen")
	}
	if bob.MixSkipReason == "" {
		t.Fatal("bob's report does not say why the mix was left alone")
	}
	// The keys are the ones a reader outside this package will look for.
	for _, want := range []string{`"mix_spliced"`, `"windows"`, `"from_ms"`, `"crossfade_ms"`, `"render_hz"`, `"mix_skip_reason"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the manifest has no %s key:\n%s", want, raw)
		}
	}
}
