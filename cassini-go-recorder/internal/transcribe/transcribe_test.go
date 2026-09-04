package transcribe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

	const sourceDurationMS = int64(1_977_527)
	const playableDurationMS = int64(242_413)
	additional := []AdditionalTranscript{{
		ID:      "parakeet-v2",
		Path:    "transcript.parakeet-v2.json",
		ModelID: ModelParakeet06BV3,
	}}
	if err := WriteManifest(path, ManifestInput{SrcBasename: "source.mkv", SrcDurationMS: sourceDurationMS, DigestDurationMS: playableDurationMS, Streams: streams, Segments: segments, STTBackend: "fake-engine", STTModelID: ModelParakeet06B, STTDevice: "cuda", Additional: additional}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var payload struct {
		SegmentCount     int   `json:"segmentCount"`
		DigestDurationMS int64 `json:"digestDurationMs"`
		Files            struct {
			Transcripts []artifactTranscriptRef `json:"transcripts"`
		} `json:"files"`
		Provenance struct {
			SpeechToText *provStep `json:"speechToText"`
		} `json:"provenance"`
		Source struct {
			DurationMS int64 `json:"durationMs"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if payload.SegmentCount != 1 {
		t.Fatalf("expected segmentCount 1, got %d", payload.SegmentCount)
	}
	if payload.Source.DurationMS != sourceDurationMS {
		t.Fatalf("expected source.durationMs %d, got %d", sourceDurationMS, payload.Source.DurationMS)
	}
	if payload.DigestDurationMS != playableDurationMS {
		t.Fatalf("expected digestDurationMs %d, got %d", playableDurationMS, payload.DigestDurationMS)
	}
	if payload.Provenance.SpeechToText == nil || payload.Provenance.SpeechToText.Device != "cuda" {
		t.Fatalf("expected speechToText.device cuda, got %#v", payload.Provenance.SpeechToText)
	}
	if len(payload.Files.Transcripts) != 2 {
		t.Fatalf("expected primary and additional transcript provenance, got %d entries", len(payload.Files.Transcripts))
	}
	if payload.Provenance.SpeechToText.Backend != "fake-engine" {
		t.Errorf("speechToText.backend = %q, want the resolved engine fake-engine",
			payload.Provenance.SpeechToText.Backend)
	}
	for _, transcript := range payload.Files.Transcripts {
		if transcript.Provenance == nil || transcript.Provenance.Device != "cuda" {
			t.Errorf("transcript %q device provenance = %#v, want cuda", transcript.ID, transcript.Provenance)
		}
		if transcript.Provenance != nil && transcript.Provenance.Backend != "fake-engine" {
			t.Errorf("transcript %q backend provenance = %q, want fake-engine",
				transcript.ID, transcript.Provenance.Backend)
		}
	}
}
