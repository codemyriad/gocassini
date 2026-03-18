package transcribe

import (
	"bytes"
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

	hasReadable, err := writeReadableArtifacts(tmp, streams, segments, 900, "abc123", cfg, &stdout)
	if err != nil {
		t.Fatalf("expected cleanup failure to be skipped, got %v", err)
	}
	if hasReadable {
		t.Fatalf("expected no readable artifacts when cleanup fails")
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

	hasReadable, err := writeReadableArtifacts(tmp, streams, segments, 900, "abc123", cfg, &stdout)
	if err == nil {
		t.Fatalf("expected strict cleanup failure to abort")
	}
	if hasReadable {
		t.Fatalf("expected readable artifacts to be absent on strict failure")
	}
	if !strings.Contains(err.Error(), "readable cleanup: boom") {
		t.Fatalf("expected wrapped cleanup error, got %v", err)
	}
	if strings.Contains(stdout.String(), "warn:") {
		t.Fatalf("expected strict failure without warning fallback, got %q", stdout.String())
	}
}
