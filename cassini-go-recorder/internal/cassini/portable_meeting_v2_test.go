package cassini

import (
	"strings"
	"testing"

	"gocassini/internal/portable"
)

func TestPortableMeetingV2Enabled(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "On"} {
		t.Setenv("CASSINI_FORMAT_V2", value)
		if !portableMeetingV2Enabled() {
			t.Errorf("CASSINI_FORMAT_V2=%q should enable v2", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv("CASSINI_FORMAT_V2", value)
		if portableMeetingV2Enabled() {
			t.Errorf("CASSINI_FORMAT_V2=%q should not enable v2", value)
		}
	}
}

func TestBuildPortableMeetingV2TagsSyntheszisesRawASREntry(t *testing.T) {
	manifest := portable.NormalizeManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID:             "mtg_" + strings.Repeat("a", 64),
			Title:          "Test",
			CreatedAtUTC:   "2026-05-12T10:00:00Z",
			ProcessedAtUTC: "2026-05-12T10:05:00Z",
			DurationMS:     1000,
		},
		Audio: portable.Audio{
			Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy, PCMFormat: portable.AudioPCMFormat,
			PCMSHA256: strings.Repeat("a", 64), SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Speakers: []portable.Speaker{{ID: "spk_0", Label: "Alice"}},
		Transcript: portable.Transcript{
			Format: "cassini.words.v1", Language: "en", WordCount: 2,
			Items: []portable.TranscriptItem{
				{Speaker: "spk_0", StartMS: 0, EndMS: 400, Text: "hello"},
				{Speaker: "spk_0", StartMS: 400, EndMS: 800, Text: "world"},
			},
		},
		Provenance: &portable.Provenance{
			SpeechToText: &portable.ProcessingStep{
				Backend: "sherpa-onnx-go", Engine: "sherpa-onnx", Model: "parakeet-tdt-0.6b-v2-int8",
			},
		},
	})

	tags, err := buildPortableMeetingV2Tags(manifest)
	if err != nil {
		t.Fatalf("buildPortableMeetingV2Tags: %v", err)
	}
	if tags["CASSINI_FORMAT"] != portable.FormatV2 {
		t.Errorf("CASSINI_FORMAT = %q, want %q", tags["CASSINI_FORMAT"], portable.FormatV2)
	}
	if tags["CASSINI_TRANSCRIPT_IDS"] != "raw-asr" {
		t.Errorf("CASSINI_TRANSCRIPT_IDS = %q, want %q", tags["CASSINI_TRANSCRIPT_IDS"], "raw-asr")
	}
	if tags["CASSINI_TRANSCRIPT_DEFAULT"] != "raw-asr" {
		t.Errorf("CASSINI_TRANSCRIPT_DEFAULT = %q", tags["CASSINI_TRANSCRIPT_DEFAULT"])
	}
	if _, ok := tags["CASSINI_TX_RAW_ASR_PAYLOAD_CHUNK_COUNT"]; !ok {
		t.Errorf("missing CASSINI_TX_RAW_ASR_PAYLOAD_CHUNK_COUNT")
	}
	if _, ok := tags["CASSINI_TX_RAW_ASR_PAYLOAD_000"]; !ok {
		t.Errorf("missing first chunk of raw-asr body")
	}
}
