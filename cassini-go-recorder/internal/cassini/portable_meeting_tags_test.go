package cassini

import (
	"encoding/json"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

func TestBuildPortableMeetingTagsSynthesizesRawASREntry(t *testing.T) {
	manifest := portable.NormalizePublishedManifest(portable.Manifest{
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
			MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: strings.Repeat("a", 64),
			SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Speakers: []portable.Speaker{{ID: "spk_0", Label: "Alice"}},
		Provenance: &portable.Provenance{
			SpeechToText: &portable.ProcessingStep{
				Backend: "sherpa-onnx-go", Engine: "sherpa-onnx", Model: "parakeet-model",
			},
		},
	})
	source := portableMeetingSource{Transcript: portableTranscriptArtifact{
		Segments: []portableTranscriptSegment{{
			Speaker: "spk_0", StartMS: 0, EndMS: 800,
			Words: []portableTranscriptWord{
				{Text: "hello", StartMS: 0, EndMS: 400},
				{Text: "world", StartMS: 400, EndMS: 800},
			},
		}},
	}}

	tags, err := buildPortableMeetingTagsFromSource(manifest, source)
	if err != nil {
		t.Fatalf("buildPortableMeetingTagsFromSource: %v", err)
	}
	if tags["CASSINI_FORMAT"] != portable.Format {
		t.Errorf("CASSINI_FORMAT = %q, want %q", tags["CASSINI_FORMAT"], portable.Format)
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

func TestBuildPortableMeetingTagsFromSourceMultiTranscript(t *testing.T) {
	manifest := portable.NormalizePublishedManifest(portable.Manifest{
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
			MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: strings.Repeat("b", 64),
			SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Speakers: []portable.Speaker{{ID: "spk_0", Label: "Alice"}},
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
				Provenance: &portable.ProcessingStep{Engine: "sherpa-onnx", Model: "parakeet-model"},
			},
			{
				ID: "canary", Role: portable.RoleRawASR, Default: true, Language: "en",
				Transcript: makeArt("canary-hello"),
				Provenance: &portable.ProcessingStep{Engine: "sherpa-onnx", Model: "canary-model"},
			},
		},
	}

	tags, err := buildPortableMeetingTagsFromSource(manifest, source)
	if err != nil {
		t.Fatalf("buildPortableMeetingTagsFromSource: %v", err)
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

func TestAssembleTranscriptInputsCarriesPublishedReadableAndDisplayBodies(t *testing.T) {
	manifest := portable.NormalizePublishedManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID:             "mtg_" + strings.Repeat("c", 64),
			Title:          "Derived transcripts",
			CreatedAtUTC:   "2026-05-12T10:00:00Z",
			ProcessedAtUTC: "2026-05-12T10:05:00Z",
			DurationMS:     1000,
			Language:       "en",
		},
		Audio: portable.Audio{
			Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: strings.Repeat("c", 64),
			SampleRate: 48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Speakers: []portable.Speaker{{ID: "spk_0", Label: "Alice"}},
		Provenance: &portable.Provenance{
			DisplayTranscript: &portable.ProcessingStep{Backend: "cassini", Model: "display-v1"},
		},
	})
	display := map[string]any{
		"version": "transcript.display.v1",
		"blocks":  []any{map[string]any{"id": "d1", "text": "Hello."}},
	}
	source := portableMeetingSource{
		Transcript: portableTranscriptArtifact{
			Segments: []portableTranscriptSegment{{
				Speaker: "spk_0", StartMS: 0, EndMS: 400,
				Words: []portableTranscriptWord{{Text: "Hello.", StartMS: 0, EndMS: 400}},
			}},
		},
		DisplayTranscript: display,
	}

	inputs, defaultID, err := assembleTranscriptInputs(manifest, source)
	if err != nil {
		t.Fatalf("assembleTranscriptInputs: %v", err)
	}
	if defaultID != portable.RoleRawASR {
		t.Fatalf("default transcript = %q, want %q", defaultID, portable.RoleRawASR)
	}
	if len(inputs) != 2 {
		t.Fatalf("inputs = %d, want 2", len(inputs))
	}
	if inputs[1].Role != portable.RoleDisplay || inputs[1].SourceTranscriptID != defaultID {
		t.Fatalf("display descriptor = %+v", inputs[1])
	}

	encoded, err := portable.EncodePublishedManifest(manifest, inputs, portable.DefaultPayloadChunkSize)
	if err != nil {
		t.Fatalf("EncodePublishedManifest: %v", err)
	}
	if len(encoded.ReadableTranscripts) != 1 {
		t.Fatalf("encoded derived transcripts = %d, want 1", len(encoded.ReadableTranscripts))
	}
	for idx, want := range []map[string]any{display} {
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(encoded.ReadableTranscripts[idx].Payload.JSON); got != string(wantJSON) {
			t.Errorf("derived body %d = %s, want %s", idx, got, wantJSON)
		}
	}
}

func TestPickDefaultWordsTranscriptID(t *testing.T) {
	inputs := []portable.TranscriptInput{
		{ID: "qwen", Role: portable.RoleDisplay},
		{ID: "parakeet", Role: portable.RoleRawASR},
		{ID: "canary", Role: portable.RoleRawASR, Default: true},
	}
	if got := pickDefaultWordsTranscriptID(inputs); got != "canary" {
		t.Errorf("expected canary default, got %q", got)
	}

	// No explicit default → first raw-ASR
	inputs2 := []portable.TranscriptInput{
		{ID: "qwen", Role: portable.RoleDisplay},
		{ID: "parakeet", Role: portable.RoleRawASR},
	}
	if got := pickDefaultWordsTranscriptID(inputs2); got != "parakeet" {
		t.Errorf("expected parakeet default, got %q", got)
	}

	// No raw-ASR at all → empty
	inputs3 := []portable.TranscriptInput{
		{ID: "qwen", Role: portable.RoleDisplay},
	}
	if got := pickDefaultWordsTranscriptID(inputs3); got != "" {
		t.Errorf("expected empty default, got %q", got)
	}
}
