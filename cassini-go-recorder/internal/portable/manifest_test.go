package portable

import "testing"

func TestBuildOpusTagsIncludesProcessingProvenanceSummary(t *testing.T) {
	manifest := NormalizeManifest(Manifest{
		Meeting: Meeting{
			ID:              "meeting-1",
			Title:           "Weekly Sync",
			CreatedAtUTC:    "2026-03-13T10:00:00Z",
			RecordedAtLocal: "2026-03-13T11:00:00",
			ProcessedAtUTC:  "2026-03-13T10:02:00Z",
			DurationMS:      1000,
		},
		Audio: Audio{
			Container:   "ogg",
			Codec:       "opus",
			SampleRate:  48000,
			Channels:    1,
			SampleCount: 48000,
			DurationMS:  1000,
		},
		Integrity: Integrity{
			MatchPolicy: AudioMatchPolicy,
			PCMFormat:   AudioPCMFormat,
			PCMSHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			SampleRate:  48000,
			Channels:    1,
			SampleCount: 48000,
			DurationMS:  1000,
		},
		Speakers: []Speaker{{ID: "spk_1", Label: "Speaker 1"}},
		Transcript: Transcript{
			Format:    "cassini.words.v1",
			WordCount: 1,
			Items: []TranscriptItem{
				{Speaker: "spk_1", StartMS: 0, EndMS: 100, Text: "hello"},
			},
		},
		Provenance: &Provenance{
			SpeechToText: &ProcessingStep{
				Backend:  "local-whisper",
				Engine:   "faster-whisper",
				Model:    "large-v3",
				Device:   "cuda",
				Language: "en",
			},
			ReadableCleanup: &ProcessingStep{
				Backend: "local-llama-cli",
				Engine:  "llama.cpp",
				Model:   "model-Q4_K_M.gguf",
				Source:  "generated",
			},
		},
	})
	payload := EncodedPayload{
		Chunks:          []string{"abc"},
		SHA256:          "feedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface",
		RawBytes:        123,
		CompressedBytes: 45,
	}

	tags := BuildOpusTags(manifest, payload)

	if got := tags["CASSINI_STT_ENGINE"]; got != "faster-whisper" {
		t.Fatalf("expected CASSINI_STT_ENGINE, got %q", got)
	}
	if got := tags["CASSINI_STT_MODEL"]; got != "large-v3" {
		t.Fatalf("expected CASSINI_STT_MODEL, got %q", got)
	}
	if got := tags["CASSINI_READABLE_ENGINE"]; got != "llama.cpp" {
		t.Fatalf("expected CASSINI_READABLE_ENGINE, got %q", got)
	}
	if got := tags["CASSINI_READABLE_SOURCE"]; got != "generated" {
		t.Fatalf("expected CASSINI_READABLE_SOURCE, got %q", got)
	}
	if got := tags["CASSINI_RECORDED_AT_LOCAL"]; got != "2026-03-13T11:00:00" {
		t.Fatalf("expected CASSINI_RECORDED_AT_LOCAL, got %q", got)
	}
	if got := tags["CASSINI_PROCESSED_AT"]; got != "2026-03-13T10:02:00Z" {
		t.Fatalf("expected CASSINI_PROCESSED_AT, got %q", got)
	}
}
