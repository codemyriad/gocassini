package transcribe

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReadableArtifactsSkipsCleanupFailuresByDefault(t *testing.T) {
	tmp := t.TempDir()

	cfg := BuildConfig{
		LLM: LLMConfig{
			APIKey:  "test-key",
			BaseURL: "https://example.test/api/v1",
			Model:   "test-model",
		},
	}
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{
		{
			SpeakerID: "spk_alex",
			StartMS:   0,
			EndMS:     900,
			Text:      "uh hello there",
			Words: []Word{
				{Text: "uh", StartMS: 0, EndMS: 100},
				{Text: "hello", StartMS: 120, EndMS: 500},
				{Text: "there", StartMS: 520, EndMS: 900},
			},
		},
	}

	var stdout bytes.Buffer
	prev := readableCleanupFn
	readableCleanupFn = func(LLMConfig, []Segment) ([]Segment, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { readableCleanupFn = prev })

	cleaned, hasReadable, err := writeReadableArtifacts(tmp, streams, segments, 900, "abc123", cfg, &stdout)
	if err != nil {
		t.Fatalf("expected cleanup failure to be skipped, got %v", err)
	}
	if hasReadable {
		t.Fatalf("expected no readable artifacts when cleanup fails")
	}
	if cleaned != nil {
		t.Fatalf("expected nil cleaned segments when cleanup fails, got %v", cleaned)
	}
	if !strings.Contains(stdout.String(), "warn: LLM cleanup failed: boom") {
		t.Fatalf("expected warning in stdout, got %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(tmp, "transcript.readable.v1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected readable transcript to be skipped, stat err=%v", err)
	}
}

func TestWriteReadableArtifactsPassesConfiguredAndSpeakerPreferredSpellings(t *testing.T) {
	tmp := t.TempDir()
	cfg := BuildConfig{
		LLM: LLMConfig{
			APIKey:  "test-key",
			BaseURL: "https://example.test/api/v1",
			Model:   "test-model",
		},
		TranscriptionTerms: []string{"Gocassini", " alice "},
	}
	streams := []AudioStream{
		{SpeakerID: "spk_alice", SpeakerLabel: "Alice"},
		{SpeakerID: "spk_recorder", SpeakerLabel: "Cassini Recorder"},
	}
	segments := []Segment{
		{
			SpeakerID: "spk_alice",
			StartMS:   0,
			EndMS:     900,
			Text:      "hello there",
			Words: []Word{
				{Text: "hello", StartMS: 0, EndMS: 400},
				{Text: "there", StartMS: 450, EndMS: 900},
			},
		},
	}

	var gotTerms []string
	prev := readableCleanupFn
	readableCleanupFn = func(cfg LLMConfig, input []Segment) ([]Segment, error) {
		gotTerms = append([]string(nil), cfg.PreferredSpellings...)
		return append([]Segment(nil), input...), nil
	}
	t.Cleanup(func() { readableCleanupFn = prev })

	if _, _, err := writeReadableArtifacts(tmp, streams, segments, 900, "abc123", cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("writeReadableArtifacts() error = %v", err)
	}
	want := "Gocassini|alice|Cassini Recorder"
	if got := strings.Join(gotTerms, "|"); got != want {
		t.Fatalf("PreferredSpellings = %q, want %q", got, want)
	}
}

func TestWriteReadableArtifactsFailsWhenStrict(t *testing.T) {
	tmp := t.TempDir()

	cfg := BuildConfig{
		LLM: LLMConfig{
			APIKey:  "test-key",
			BaseURL: "https://example.test/api/v1",
			Model:   "test-model",
		},
		StrictReadableCleanup: true,
	}
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{
		{
			SpeakerID: "spk_alex",
			StartMS:   0,
			EndMS:     900,
			Text:      "uh hello there",
			Words: []Word{
				{Text: "uh", StartMS: 0, EndMS: 100},
				{Text: "hello", StartMS: 120, EndMS: 500},
				{Text: "there", StartMS: 520, EndMS: 900},
			},
		},
	}

	var stdout bytes.Buffer
	prev := readableCleanupFn
	readableCleanupFn = func(LLMConfig, []Segment) ([]Segment, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { readableCleanupFn = prev })

	cleaned, hasReadable, err := writeReadableArtifacts(tmp, streams, segments, 900, "abc123", cfg, &stdout)
	if err == nil {
		t.Fatalf("expected strict cleanup failure to abort")
	}
	if hasReadable {
		t.Fatalf("expected readable artifacts to be absent on strict failure")
	}
	if cleaned != nil {
		t.Fatalf("expected nil cleaned segments on strict failure, got %v", cleaned)
	}
	if !strings.Contains(err.Error(), "readable cleanup: boom") {
		t.Fatalf("expected wrapped cleanup error, got %v", err)
	}
	if strings.Contains(stdout.String(), "warn:") {
		t.Fatalf("expected strict failure without warning fallback, got %q", stdout.String())
	}
}

func TestWriteReadableArtifactsWritesReadableTranscriptVersion(t *testing.T) {
	tmp := t.TempDir()

	cfg := BuildConfig{
		LLM: LLMConfig{
			APIKey:  "test-key",
			BaseURL: "https://example.test/api/v1",
			Model:   "test-model",
		},
	}
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{
		{
			SpeakerID: "spk_alex",
			StartMS:   0,
			EndMS:     900,
			Text:      "uh hello there",
			Words: []Word{
				{Text: "uh", StartMS: 0, EndMS: 100},
				{Text: "hello", StartMS: 120, EndMS: 500},
				{Text: "there", StartMS: 520, EndMS: 900},
			},
		},
	}

	prev := readableCleanupFn
	readableCleanupFn = func(LLMConfig, []Segment) ([]Segment, error) {
		return []Segment{
			{
				SpeakerID: "spk_alex",
				StartMS:   0,
				EndMS:     900,
				Text:      "Hello there.",
				Words: []Word{
					{Text: "Hello", StartMS: 0, EndMS: 450},
					{Text: "there.", StartMS: 450, EndMS: 900},
				},
			},
		}, nil
	}
	t.Cleanup(func() { readableCleanupFn = prev })

	cleaned, hasReadable, err := writeReadableArtifacts(tmp, streams, segments, 900, "abc123", cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("expected readable artifacts, got %v", err)
	}
	if !hasReadable {
		t.Fatalf("expected readable artifacts to be reported")
	}
	if len(cleaned) == 0 {
		t.Fatalf("expected cleaned segments to be returned")
	}
	if cleaned[0].Text != "Hello there." {
		t.Fatalf("expected cleaned text on returned segment, got %q", cleaned[0].Text)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "transcript.readable.v1.json"))
	if err != nil {
		t.Fatalf("read readable transcript: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse readable transcript: %v", err)
	}
	if got := payload["version"]; got != readableTranscriptVersion {
		t.Fatalf("expected readable transcript version, got %#v", got)
	}
	if got := payload["sourceTranscriptVersion"]; got != transcriptWordsVersion {
		t.Fatalf("expected source transcript version %q, got %#v", transcriptWordsVersion, got)
	}
	segmentsValue, ok := payload["segments"].([]any)
	if !ok || len(segmentsValue) != 1 {
		t.Fatalf("expected one readable segment, got %#v", payload["segments"])
	}
	firstSegment, ok := segmentsValue[0].(map[string]any)
	if !ok {
		t.Fatalf("expected object readable segment, got %#v", segmentsValue[0])
	}
	if got := firstSegment["id"]; got != "r_seg_000000" {
		t.Fatalf("expected readable segment id r_seg_000000, got %#v", got)
	}
	sourceIDs, ok := firstSegment["sourceSegmentIds"].([]any)
	if !ok || len(sourceIDs) != 1 || sourceIDs[0] != "seg_000000" {
		t.Fatalf("expected readable sourceSegmentIds [seg_000000], got %#v", firstSegment["sourceSegmentIds"])
	}
	if _, ok := firstSegment["words"]; ok {
		t.Fatalf("expected readable segment to omit words, got %#v", firstSegment["words"])
	}
}

func TestWriteTranscriptWithHashWritesViewerCompatibleTranscriptContract(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.words.v1.json")
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{
		{
			SpeakerID: "spk_alex",
			StartMS:   0,
			EndMS:     900,
			Text:      "hello there",
			Words: []Word{
				{Text: "hello", StartMS: 0, EndMS: 400},
				{Text: "there", StartMS: 450, EndMS: 900},
			},
		},
	}

	if err := writeTranscriptWithHash(path, transcriptWordsVersion, streams, segments, 900, "abc123"); err != nil {
		t.Fatalf("write transcript with hash: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}

	var payload struct {
		Version string `json:"version"`
		Media   struct {
			SHA256 string `json:"sha256"`
		} `json:"media"`
		Segments []struct {
			ID    string `json:"id"`
			Words []struct {
				ID string `json:"id"`
			} `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	if payload.Version != transcriptWordsVersion {
		t.Fatalf("expected transcript version %q, got %q", transcriptWordsVersion, payload.Version)
	}
	if payload.Media.SHA256 != "abc123" {
		t.Fatalf("expected transcript sha256 abc123, got %q", payload.Media.SHA256)
	}
	if len(payload.Segments) != 1 {
		t.Fatalf("expected one transcript segment, got %d", len(payload.Segments))
	}
	if payload.Segments[0].ID != "seg_000000" {
		t.Fatalf("expected transcript segment id seg_000000, got %q", payload.Segments[0].ID)
	}
	if len(payload.Segments[0].Words) != 2 {
		t.Fatalf("expected two transcript words, got %d", len(payload.Segments[0].Words))
	}
	if payload.Segments[0].Words[0].ID != "seg_000000:w_0" {
		t.Fatalf("expected first transcript word id seg_000000:w_0, got %q", payload.Segments[0].Words[0].ID)
	}
}

func TestWriteManifestIncludesRuntimeSummaryFields(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{
		{
			SpeakerID: "spk_alex",
			StartMS:   0,
			EndMS:     900,
			Text:      "hello there",
			Words: []Word{
				{Text: "hello", StartMS: 0, EndMS: 400},
				{Text: "there", StartMS: 450, EndMS: 900},
			},
		},
	}

	if err := WriteManifest(path, "source.mkv", 1200, streams, segments, ModelParakeet06B, "openai/gpt-4o-mini", true, "", false, nil); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var payload struct {
		SegmentCount     int   `json:"segmentCount"`
		DigestDurationMS int64 `json:"digestDurationMs"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if payload.SegmentCount != 1 {
		t.Fatalf("expected segmentCount 1, got %d", payload.SegmentCount)
	}
	if payload.DigestDurationMS != 1200 {
		t.Fatalf("expected digestDurationMs 1200, got %d", payload.DigestDurationMS)
	}
}
