package cassini

import (
	"strings"
	"testing"

	"gocassini/internal/portable"
)

func TestPortableMeetingV2Enabled(t *testing.T) {
	// v2 is the default — empty/falsy/unrecognized CASSINI_FORMAT_V1 leaves
	// v2 on.
	for _, value := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv("CASSINI_FORMAT_V1", value)
		if !portableMeetingV2Enabled() {
			t.Errorf("CASSINI_FORMAT_V1=%q should leave v2 enabled", value)
		}
	}
	// CASSINI_FORMAT_V1=truthy opts back into v1.
	for _, value := range []string{"1", "true", "TRUE", "yes", "On"} {
		t.Setenv("CASSINI_FORMAT_V1", value)
		if portableMeetingV2Enabled() {
			t.Errorf("CASSINI_FORMAT_V1=%q should disable v2 (use v1)", value)
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

func TestBuildPortableMeetingV2TagsFromSourceMultiTranscript(t *testing.T) {
	manifest := portable.NormalizeManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID:             "mtg_" + strings.Repeat("b", 64),
			Title:          "Two engines",
			CreatedAtUTC:   "2026-05-12T10:00:00Z",
			ProcessedAtUTC: "2026-05-12T10:05:00Z",
			DurationMS:     1000,
		},
		Audio: portable.Audio{
			Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy, PCMFormat: portable.AudioPCMFormat,
			PCMSHA256: strings.Repeat("b", 64), SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Speakers: []portable.Speaker{{ID: "spk_0", Label: "Alice"}},
		Transcript: portable.Transcript{
			Format: "cassini.words.v1", WordCount: 0, Items: []portable.TranscriptItem{},
		},
	})

	makeArt := func(text string) portableTranscriptArtifact {
		return portableTranscriptArtifact{
			Segments: []portableTranscriptSegment{
				{
					Speaker: "spk_0", StartMS: 0, EndMS: 400,
					Words: []portableTranscriptWord{{Text: text, StartMS: 0, EndMS: 400}},
				},
			},
		}
	}
	source := portableMeetingSource{
		AdditionalTranscripts: []portableNamedTranscript{
			{
				ID: "parakeet", Role: portable.RoleRawASR, Default: false, Language: "en",
				Transcript: makeArt("parakeet-hello"),
				Provenance: &portable.ProcessingStep{Engine: "sherpa-onnx", Model: "parakeet-tdt-0.6b-v2-int8"},
			},
			{
				ID: "canary", Role: portable.RoleRawASR, Default: true, Language: "en",
				Transcript: makeArt("canary-hello"),
				Provenance: &portable.ProcessingStep{Engine: "sherpa-onnx", Model: "canary-1b-v2"},
			},
		},
	}

	tags, err := buildPortableMeetingV2TagsFromSource(manifest, source)
	if err != nil {
		t.Fatalf("buildPortableMeetingV2TagsFromSource: %v", err)
	}

	if got := tags["CASSINI_TRANSCRIPT_IDS"]; got != "canary,parakeet" {
		t.Errorf("CASSINI_TRANSCRIPT_IDS = %q, want %q", got, "canary,parakeet")
	}
	if got := tags["CASSINI_TRANSCRIPT_DEFAULT"]; got != "canary" {
		t.Errorf("CASSINI_TRANSCRIPT_DEFAULT = %q", got)
	}
	for _, key := range []string{
		"CASSINI_TX_PARAKEET_PAYLOAD_CHUNK_COUNT",
		"CASSINI_TX_PARAKEET_PAYLOAD_000",
		"CASSINI_TX_PARAKEET_PAYLOAD_SHA256",
		"CASSINI_TX_CANARY_PAYLOAD_CHUNK_COUNT",
		"CASSINI_TX_CANARY_PAYLOAD_000",
		"CASSINI_TX_CANARY_PAYLOAD_SHA256",
	} {
		if _, ok := tags[key]; !ok {
			t.Errorf("missing tag %q", key)
		}
	}
	// parakeet and canary must have different bodies → different sha256s.
	if tags["CASSINI_TX_PARAKEET_PAYLOAD_SHA256"] == tags["CASSINI_TX_CANARY_PAYLOAD_SHA256"] {
		t.Errorf("parakeet and canary sha256 should differ; both equal %q", tags["CASSINI_TX_PARAKEET_PAYLOAD_SHA256"])
	}
}

func TestPickV2DefaultRawID(t *testing.T) {
	inputs := []portable.TranscriptInput{
		{ID: "qwen", Role: portable.RoleReadableCleanup},
		{ID: "parakeet", Role: portable.RoleRawASR},
		{ID: "canary", Role: portable.RoleRawASR, Default: true},
	}
	if got := pickV2DefaultRawID(inputs); got != "canary" {
		t.Errorf("expected canary default, got %q", got)
	}

	// No explicit default → first raw-ASR
	inputs2 := []portable.TranscriptInput{
		{ID: "qwen", Role: portable.RoleReadableCleanup},
		{ID: "parakeet", Role: portable.RoleRawASR},
	}
	if got := pickV2DefaultRawID(inputs2); got != "parakeet" {
		t.Errorf("expected parakeet default, got %q", got)
	}

	// No raw-ASR at all → empty
	inputs3 := []portable.TranscriptInput{
		{ID: "qwen", Role: portable.RoleReadableCleanup},
	}
	if got := pickV2DefaultRawID(inputs3); got != "" {
		t.Errorf("expected empty default, got %q", got)
	}
}
