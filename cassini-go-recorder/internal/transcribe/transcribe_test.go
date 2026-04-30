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

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse readable transcript: %v", err)
	}
	if payload.Version != "transcript.readable.v1" {
		t.Fatalf("expected readable transcript version, got %q", payload.Version)
	}
}
