package portable

import (
	"encoding/json"
	"testing"
)

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

// roomTagFixture is the smallest manifest that carries a room, for the tag
// tests below. Only Meeting matters to them.
func roomTagFixture(room Meeting) Manifest {
	room.ID = "meeting-1"
	room.CreatedAtUTC = "2026-03-13T10:00:00Z"
	room.DurationMS = 1000
	return NormalizeManifest(Manifest{Meeting: room})
}

func TestBuildOpusTagsEmitsRoomTagsOnlyWhenKnown(t *testing.T) {
	payload := EncodedPayload{Chunks: []string{"abc"}}

	// The room is already inside the gzipped payload; these plain tags exist so
	// a shell reader can get at it with one ffprobe call (D-622).
	withRoom := BuildOpusTags(roomTagFixture(Meeting{
		Title: "Weekly Sync", RoomID: "a7bc3k9x", RoomName: "Weekly Sync",
	}), payload)
	if got := withRoom["CASSINI_ROOM_ID"]; got != "a7bc3k9x" {
		t.Errorf("CASSINI_ROOM_ID = %q, want %q", got, "a7bc3k9x")
	}
	if got := withRoom["CASSINI_ROOM_NAME"]; got != "Weekly Sync" {
		t.Errorf("CASSINI_ROOM_NAME = %q, want %q", got, "Weekly Sync")
	}

	// Absent, not empty: an empty id would read as "this meeting has a room
	// whose id is the empty string".
	withoutRoom := BuildOpusTags(roomTagFixture(Meeting{Title: "Some File"}), payload)
	for _, tag := range []string{"CASSINI_ROOM_ID", "CASSINI_ROOM_NAME"} {
		if _, ok := withoutRoom[tag]; ok {
			t.Errorf("%s is present on a meeting with no room, want absent", tag)
		}
	}

	// A room whose name was resolved but whose token was not (a non-Talk source,
	// or a recording packed by hand) must still get the half that is known.
	nameOnly := BuildOpusTags(roomTagFixture(Meeting{Title: "X", RoomName: "Old Standup"}), payload)
	if _, ok := nameOnly["CASSINI_ROOM_ID"]; ok {
		t.Errorf("CASSINI_ROOM_ID is present with no room id, want absent")
	}
	if got := nameOnly["CASSINI_ROOM_NAME"]; got != "Old Standup" {
		t.Errorf("CASSINI_ROOM_NAME = %q, want %q", got, "Old Standup")
	}
}

func TestManifestRoomSurvivesTheEncodedPayload(t *testing.T) {
	manifest := roomTagFixture(Meeting{Title: "Weekly Sync", RoomID: "a7bc3k9x", RoomName: "Weekly Sync"})

	payload, err := EncodeManifest(manifest, 0)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	var decoded Manifest
	if err := json.Unmarshal(payload.JSON, &decoded); err != nil {
		t.Fatalf("decode payload JSON: %v", err)
	}
	if decoded.Meeting.RoomID != "a7bc3k9x" || decoded.Meeting.RoomName != "Weekly Sync" {
		t.Errorf("payload meeting room = %q/%q, want %q/%q",
			decoded.Meeting.RoomID, decoded.Meeting.RoomName, "a7bc3k9x", "Weekly Sync")
	}

	// omitempty, so a meeting with no room does not ship two empty strings that
	// a consumer would have to distinguish from a real value.
	empty, err := EncodeManifest(roomTagFixture(Meeting{Title: "X"}), 0)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(empty.JSON, &raw); err != nil {
		t.Fatalf("decode payload JSON: %v", err)
	}
	meeting, _ := raw["meeting"].(map[string]any)
	for _, key := range []string{"roomId", "roomName"} {
		if _, ok := meeting[key]; ok {
			t.Errorf("meeting.%s is present with no room, want omitted", key)
		}
	}
}
