package cassini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPortableMeetingSourceIncludesReadableAndDisplayTranscripts(t *testing.T) {
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
	writePortableJSONFixture(t, filepath.Join(root, "transcript.display.v1.json"), map[string]any{
		"version": "transcript.display.v1",
		"media": map[string]any{
			"src":        "meeting.webm",
			"durationMs": 1234,
		},
		"speakers": []map[string]any{
			{"id": "speaker-1", "label": "Speaker 1"},
		},
		"blocks": []map[string]any{
			{
				"id":                "block-1",
				"speaker":           "speaker-1",
				"speakerLabel":      "Speaker 1",
				"startMs":           0,
				"endMs":             1000,
				"text":              "Hello world.",
				"sourceSegmentIds":  []string{"seg_1"},
				"wordCount":         2,
				"timedWordCount":    2,
				"timingCoverage":    1,
				"tokens": []map[string]any{
					{
						"text":          "Hello",
						"spaceBefore":   false,
						"kind":          "word",
						"sourceWordIds": []string{"seg_1:w_0"},
						"startMs":       0,
						"endMs":         500,
						"alignment":     "source",
					},
					{
						"text":          "world",
						"spaceBefore":   true,
						"kind":          "word",
						"sourceWordIds": []string{"seg_1:w_1"},
						"startMs":       500,
						"endMs":         1000,
						"alignment":     "source",
					},
				},
			},
		},
	})
	writePortableJSONFixture(t, filepath.Join(root, "manifest.json"), map[string]any{
		"generatedAt": "2026-03-11T00:00:00Z",
		"source": map[string]any{
			"basename":        "daily-meeting-2026-03-11--12:30.mkv",
			"durationMs":      1234,
			"recordedAtLocal": "2026-03-11T12:30:00",
		},
		"provenance": map[string]any{
			"speechToText": map[string]any{
				"backend": "local-whisper",
				"engine":  "faster-whisper",
				"model":   "large-v3",
				"device":  "cuda",
			},
			"readableCleanup": map[string]any{
				"backend": "local-llama-cli",
				"engine":  "llama.cpp",
				"model":   "model-Q4_K_M.gguf",
				"source":  "generated",
			},
		},
		"files": map[string]any{
			"audio":              "meeting.webm",
			"transcript":         "transcript.words.v1.json",
			"readableTranscript": "transcript.readable.v1.json",
			"displayTranscript":  "transcript.display.v1.json",
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
	if got := source.DisplayTranscript["version"]; got != "transcript.display.v1" {
		t.Fatalf("expected display transcript to load, got %v", got)
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
	if got := manifest.DisplayTranscript["version"]; got != "transcript.display.v1" {
		t.Fatalf("expected display transcript in manifest, got %v", got)
	}
	if manifest.Provenance == nil || manifest.Provenance.SpeechToText == nil {
		t.Fatalf("expected provenance to be carried into portable manifest")
	}
	if got := manifest.Provenance.SpeechToText.Model; got != "large-v3" {
		t.Fatalf("expected speech to text model provenance, got %q", got)
	}
	if got := manifest.Meeting.RecordedAtLocal; got != "2026-03-11T12:30:00" {
		t.Fatalf("expected recordedAtLocal in portable manifest, got %q", got)
	}
	if got := manifest.Meeting.ProcessedAtUTC; got != "2026-03-11T00:00:00Z" {
		t.Fatalf("expected processedAtUtc in portable manifest, got %q", got)
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
