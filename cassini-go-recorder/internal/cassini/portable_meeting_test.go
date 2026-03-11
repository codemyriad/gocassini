package cassini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPortableMeetingSourceIncludesReadableTranscript(t *testing.T) {
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "meeting.webm"), []byte("audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	writePortableJSONFixture(t, filepath.Join(root, "transcript.words.v1.json"), map[string]any{
		"version": "transcript.words.v1",
		"media": map[string]any{
			"src":        "meeting.webm",
			"durationMs": 1234,
			"sha256":     "abc",
		},
		"speakers": []map[string]any{
			{"id": "speaker-1", "label": "Speaker 1"},
		},
		"segments": []map[string]any{
			{
				"speaker": "speaker-1",
				"startMs": 0,
				"endMs":   1000,
				"text":    "hello world",
				"words": []map[string]any{
					{"text": "hello", "startMs": 0, "endMs": 500},
					{"text": "world", "startMs": 500, "endMs": 1000},
				},
			},
		},
	})
	writePortableJSONFixture(t, filepath.Join(root, "transcript.readable.v1.json"), map[string]any{
		"version": "transcript.readable.v1",
		"segments": []map[string]any{
			{"speaker": "speaker-1", "text": "Hello world."},
		},
	})
	writePortableJSONFixture(t, filepath.Join(root, "manifest.json"), map[string]any{
		"generatedAt": "2026-03-11T00:00:00Z",
		"source": map[string]any{
			"basename":   "meeting.mkv",
			"durationMs": 1234,
		},
		"files": map[string]any{
			"audio":              "meeting.webm",
			"transcript":         "transcript.words.v1.json",
			"readableTranscript": "transcript.readable.v1.json",
		},
		"speakerCount": 1,
		"wordCount":    2,
	})

	source, err := loadPortableMeetingSource(root)
	if err != nil {
		t.Fatalf("loadPortableMeetingSource: %v", err)
	}
	if got := source.ReadableTranscript["version"]; got != "transcript.readable.v1" {
		t.Fatalf("expected readable transcript to load, got %v", got)
	}

	manifest, err := buildPortableMeetingManifest(source, portableAudioIntegrity{
		SampleRate:  48000,
		Channels:    1,
		SampleCount: 59232,
		DurationMS:  1234,
		PCMSHA256:   "pcm-sha",
	}, filepath.Join(root, "meeting.opus"), portablePackOptions{
		Title:        "Meeting",
		CreatedAtUTC: "2026-03-11T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("buildPortableMeetingManifest: %v", err)
	}
	if got := manifest.ReadableTranscript["version"]; got != "transcript.readable.v1" {
		t.Fatalf("expected readable transcript in manifest, got %v", got)
	}
}

func writePortableJSONFixture(t *testing.T, path string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fixture %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}
