package transcribe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestWriteManifestRecordsSummaryWhenPresent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{{SpeakerID: "spk_alex", StartMS: 0, EndMS: 1000, Text: "hi", Words: []Word{{Text: "hi", StartMS: 0, EndMS: 1000}}}}

	if err := WriteManifest(path, "src.mkv", 1000, streams, segments, ModelID("test-stt"), "test-llm", true, "summary-model", true, nil); err != nil {
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

func TestWriteManifestOmitsSummaryWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	streams := []AudioStream{{SpeakerID: "spk_alex", SpeakerLabel: "Alex"}}
	segments := []Segment{{SpeakerID: "spk_alex", StartMS: 0, EndMS: 1000, Text: "hi", Words: []Word{{Text: "hi", StartMS: 0, EndMS: 1000}}}}

	if err := WriteManifest(path, "src.mkv", 1000, streams, segments, ModelID("test-stt"), "", false, "", false, nil); err != nil {
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
