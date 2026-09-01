package portable

import (
	"encoding/json"
	"strings"
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
			MatchPolicy: LegacyAudioMatchPolicyPCM,
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

func TestBuildOpusTagsEmitsProvenanceTagsOnlyWhenKnown(t *testing.T) {
	payload := EncodedPayload{Chunks: []string{"abc"}}

	withJob := BuildOpusTags(roomTagFixture(Meeting{
		Title: "Weekly Sync", JobID: "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", AttemptNumber: 2,
	}), payload)
	if got := withJob["CASSINI_JOB_ID"]; got != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" {
		t.Errorf("CASSINI_JOB_ID = %q, want the job id", got)
	}
	if got := withJob["CASSINI_ATTEMPT_NUMBER"]; got != "2" {
		t.Errorf("CASSINI_ATTEMPT_NUMBER = %q, want %q", got, "2")
	}

	withoutJob := BuildOpusTags(roomTagFixture(Meeting{Title: "Some File"}), payload)
	for _, tag := range []string{"CASSINI_JOB_ID", "CASSINI_ATTEMPT_NUMBER"} {
		if _, ok := withoutJob[tag]; ok {
			t.Errorf("%s is present on a meeting packed outside the operator, want absent", tag)
		}
	}

	// Attempts are 1-based, so a zero is "nobody told us" rather than a legal
	// value — writing it would assert an attempt that cannot exist. A job id
	// with no attempt is a real state (a producer that knows one and not the
	// other), so the two are emitted independently.
	jobOnly := BuildOpusTags(roomTagFixture(Meeting{Title: "X", JobID: "01ABC", AttemptNumber: 0}), payload)
	if got := jobOnly["CASSINI_JOB_ID"]; got != "01ABC" {
		t.Errorf("CASSINI_JOB_ID = %q, want it emitted without an attempt", got)
	}
	if _, ok := jobOnly["CASSINI_ATTEMPT_NUMBER"]; ok {
		t.Errorf("CASSINI_ATTEMPT_NUMBER is present for attempt 0, want absent")
	}
}

func TestManifestProvenanceSurvivesTheEncodedPayload(t *testing.T) {
	manifest := roomTagFixture(Meeting{
		Title: "Weekly Sync", JobID: "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", AttemptNumber: 3,
	})

	encoded, err := EncodeManifest(manifest, 0)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	var decoded Manifest
	if err := json.Unmarshal(encoded.JSON, &decoded); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if decoded.Meeting.JobID != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" {
		t.Errorf("meeting.jobId = %q after a round trip, want the job id", decoded.Meeting.JobID)
	}
	if decoded.Meeting.AttemptNumber != 3 {
		t.Errorf("meeting.attemptNumber = %d after a round trip, want 3", decoded.Meeting.AttemptNumber)
	}

	// omitempty on both: a meeting with no operator lineage must not carry the
	// keys at all, so a consumer checking presence gets the right answer.
	bare, err := EncodeManifest(roomTagFixture(Meeting{Title: "Some File"}), 0)
	if err != nil {
		t.Fatalf("encode bare manifest: %v", err)
	}
	for _, key := range []string{`"jobId"`, `"attemptNumber"`} {
		if strings.Contains(string(bare.JSON), key) {
			t.Errorf("%s is present in a manifest with no operator lineage: %s", key, bare.JSON)
		}
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
