package cassini

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlattenPortableTranscriptItemsRequiresWordTimings(t *testing.T) {
	_, err := flattenPortableTranscriptItems(portableTranscriptArtifact{
		Segments: []portableTranscriptSegment{{
			Speaker: "spk_1",
			StartMS: 0,
			EndMS:   500,
			Text:    "hello world",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "text but no word timings") {
		t.Fatalf("flattenPortableTranscriptItems error = %v, want missing word timings", err)
	}
}

func TestLoadPortableMeetingSource(t *testing.T) {
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
	writePortableJSONFixture(t, filepath.Join(root, "transcript.display.v1.json"), map[string]any{
		"version": "transcript.display.v1",
		"blocks":  []map[string]any{{"id": "display-1", "text": "hello world"}},
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
			"audio":      "meeting.webm",
			"transcript": "transcript.words.v1.json",
		},
		"speakerCount": 1,
		"wordCount":    2,
	})

	source, err := loadPortableMeetingSource(root)
	if err != nil {
		t.Fatalf("loadPortableMeetingSource: %v", err)
	}
	manifest, err := buildPortableMeetingManifest(source, portableAudioIntegrity{
		SampleRate:  48000,
		Channels:    1,
		SampleCount: 59232,
		DurationMS:  1234,
		OpusSHA256:  "opus-sha",
	}, filepath.Join(root, "meeting.opus"), portablePackOptions{
		Title:        "Meeting",
		CreatedAtUTC: "2026-03-11T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("buildPortableMeetingManifest: %v", err)
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
	if got := source.DisplayTranscript["version"]; got != "transcript.display.v1" {
		t.Fatalf("display transcript version = %v", got)
	}
}

func TestPortableManifestEmbedsSummaryWhenPresent(t *testing.T) {
	root := t.TempDir()
	writePortableMinimalFixture(t, root)

	const summaryBody = "# Meeting Summary\n\n## Overview\n\nDemo overview.\n"
	if err := os.WriteFile(filepath.Join(root, "summary.md"), []byte(summaryBody), 0o644); err != nil {
		t.Fatalf("write summary.md: %v", err)
	}
	writePortableJSONFixture(t, filepath.Join(root, "manifest.json"), map[string]any{
		"generatedAt": "2026-03-11T00:00:00Z",
		"source": map[string]any{
			"basename":   "src.mkv",
			"durationMs": 1234,
		},
		"provenance": map[string]any{
			"speechToText": map[string]any{"backend": "stt", "model": "stt-model"},
			"meetingSummary": map[string]any{
				"backend": "openai-compatible",
				"model":   "summary-model",
			},
		},
		"files": map[string]any{
			"audio":      "meeting.webm",
			"transcript": "transcript.words.v1.json",
			"summary":    "summary.md",
		},
		"speakerCount": 1,
		"wordCount":    2,
	})

	source, err := loadPortableMeetingSource(root)
	if err != nil {
		t.Fatalf("loadPortableMeetingSource: %v", err)
	}
	if string(source.SummaryMarkdown) != summaryBody {
		t.Fatalf("summary markdown not loaded; got %q", string(source.SummaryMarkdown))
	}

	manifest, err := buildPortableMeetingManifest(source, portableAudioIntegrity{
		SampleRate:  48000,
		Channels:    1,
		SampleCount: 59232,
		DurationMS:  1234,
		OpusSHA256:  "opus-sha",
	}, filepath.Join(root, "meeting.opus"), portablePackOptions{Title: "Meeting"})
	if err != nil {
		t.Fatalf("buildPortableMeetingManifest: %v", err)
	}

	if got := manifest.Summary["model"]; got != "summary-model" {
		t.Errorf("manifest.Summary.model = %v, want summary-model", got)
	}
	if got := manifest.Summary["format"]; got != "markdown" {
		t.Errorf("manifest.Summary.format = %v, want markdown", got)
	}
	if got := manifest.Summary["templateVersion"]; got != "v0" {
		t.Errorf("manifest.Summary.templateVersion = %v, want v0", got)
	}

	if len(manifest.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(manifest.Attachments))
	}
	att := manifest.Attachments[0]
	if att["name"] != "summary.md" {
		t.Errorf("attachment.name = %v, want summary.md", att["name"])
	}
	if att["mime"] != "text/markdown" {
		t.Errorf("attachment.mime = %v, want text/markdown", att["mime"])
	}
	encoded, _ := att["contentBase64"].(string)
	decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if decodeErr != nil {
		t.Fatalf("decode attachment content: %v", decodeErr)
	}
	if string(decoded) != summaryBody {
		t.Errorf("attachment content mismatch: got %q want %q", string(decoded), summaryBody)
	}
}

func TestPortableManifestOmitsSummaryWhenAbsent(t *testing.T) {
	root := t.TempDir()
	writePortableMinimalFixture(t, root)
	writePortableJSONFixture(t, filepath.Join(root, "manifest.json"), map[string]any{
		"generatedAt": "2026-03-11T00:00:00Z",
		"source": map[string]any{
			"basename":   "src.mkv",
			"durationMs": 1234,
		},
		"files": map[string]any{
			"audio":      "meeting.webm",
			"transcript": "transcript.words.v1.json",
		},
		"speakerCount": 1,
		"wordCount":    2,
	})

	source, err := loadPortableMeetingSource(root)
	if err != nil {
		t.Fatalf("loadPortableMeetingSource: %v", err)
	}
	if source.SummaryMarkdown != nil {
		t.Fatalf("expected no summary loaded; got %q", string(source.SummaryMarkdown))
	}

	manifest, err := buildPortableMeetingManifest(source, portableAudioIntegrity{
		SampleRate: 48000, Channels: 1, SampleCount: 59232, DurationMS: 1234, OpusSHA256: "opus-sha",
	}, filepath.Join(root, "meeting.opus"), portablePackOptions{Title: "Meeting"})
	if err != nil {
		t.Fatalf("buildPortableMeetingManifest: %v", err)
	}
	if manifest.Summary != nil {
		t.Errorf("expected manifest.Summary nil when absent, got %v", manifest.Summary)
	}
	if len(manifest.Attachments) != 0 {
		t.Errorf("expected no attachments when no summary, got %v", manifest.Attachments)
	}
}

// writePortableMinimalFixture creates the audio + transcript files needed by
// loadPortableMeetingSource. The caller is responsible for writing manifest.json.
func writePortableMinimalFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "meeting.webm"), []byte("audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	writePortableJSONFixture(t, filepath.Join(root, "transcript.words.v1.json"), map[string]any{
		"version":  "transcript.words.v1",
		"media":    map[string]any{"src": "meeting.webm", "durationMs": 1234, "sha256": "abc"},
		"speakers": []map[string]any{{"id": "speaker-1", "label": "Speaker 1"}},
		"segments": []map[string]any{{
			"speaker": "speaker-1",
			"startMs": 0,
			"endMs":   1000,
			"text":    "hello world",
			"words": []map[string]any{
				{"text": "hello", "startMs": 0, "endMs": 500},
				{"text": "world", "startMs": 500, "endMs": 1000},
			},
		}},
	})
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

// A pack that cannot produce its output must never destroy the `.opus` that is
// already there. The commit step used to remove the destination before renaming
// the stage file over it, so a failure in that gap left the meeting with no
// portable artifact at all — and the operator now refuses to publish a meeting
// whose `.opus` is missing (D-583).
func TestCommitPortableMeetingOutputKeepsThePreviousFileWhenTheStageIsMissing(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "meeting.opus")
	if err := os.WriteFile(outPath, []byte("previous-opus"), 0o644); err != nil {
		t.Fatalf("write previous output: %v", err)
	}

	err := commitPortableMeetingOutput(filepath.Join(dir, "never-written.opus"), outPath)
	if err == nil {
		t.Fatal("expected an error when the stage file does not exist")
	}

	body, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("previous output must survive a failed commit: %v", readErr)
	}
	if string(body) != "previous-opus" {
		t.Fatalf("previous output = %q, want %q", string(body), "previous-opus")
	}
}

func TestCommitPortableMeetingOutputReplacesTheExistingFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "meeting.opus")
	stagePath := filepath.Join(dir, ".cassini-stage.opus")
	if err := os.WriteFile(outPath, []byte("previous-opus"), 0o644); err != nil {
		t.Fatalf("write previous output: %v", err)
	}
	if err := os.WriteFile(stagePath, []byte("next-opus"), 0o644); err != nil {
		t.Fatalf("write stage file: %v", err)
	}

	if err := commitPortableMeetingOutput(stagePath, outPath); err != nil {
		t.Fatalf("commitPortableMeetingOutput() error = %v", err)
	}

	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(body) != "next-opus" {
		t.Fatalf("output = %q, want %q", string(body), "next-opus")
	}
	if _, err := os.Stat(stagePath); !os.IsNotExist(err) {
		t.Fatalf("stage file must not survive the commit, err=%v", err)
	}
}
