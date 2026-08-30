package portable

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func baseManifestV2() Manifest {
	return Manifest{
		Meeting: Meeting{
			ID:           "mtg_" + strings.Repeat("a", 64),
			Title:        "Two-engine sync",
			CreatedAtUTC: "2026-05-12T10:00:00Z",
			DurationMS:   60000,
		},
		Audio: Audio{
			Container:   "ogg",
			Codec:       "opus",
			SampleRate:  48000,
			Channels:    1,
			SampleCount: 2_880_000,
			DurationMS:  60000,
		},
		Integrity: Integrity{
			MatchPolicy: LegacyAudioMatchPolicyPCM,
			PCMFormat:   AudioPCMFormat,
			PCMSHA256:   strings.Repeat("a", 64),
			SampleRate:  48000,
			Channels:    1,
			SampleCount: 2_880_000,
			DurationMS:  60000,
		},
		Speakers: []Speaker{{ID: "spk_0", Label: "Alice"}},
	}
}

func baseManifestV3() Manifest {
	manifest := baseManifestV2()
	manifest.Integrity = Integrity{
		MatchPolicy: AudioMatchPolicy,
		OpusSHA256:  strings.Repeat("b", 64),
		SampleRate:  48000,
		Channels:    1,
		SampleCount: 2_880_000,
		DurationMS:  60000,
	}
	return manifest
}

func sampleBody(speaker string, words ...string) TranscriptBody {
	items := make([]TranscriptItem, 0, len(words))
	for i, word := range words {
		items = append(items, TranscriptItem{
			Speaker: speaker,
			StartMS: int64(i * 400),
			EndMS:   int64(i*400 + 300),
			Text:    word,
		})
	}
	return TranscriptBody{
		Format:    "cassini.words.v1",
		Language:  "en",
		WordCount: len(words),
		Items:     items,
	}
}

func TestValidateTranscriptID(t *testing.T) {
	cases := []struct {
		id      string
		wantErr bool
	}{
		{"parakeet", false},
		{"canary", false},
		{"raw-asr", false},
		{"readable-qwen", false},
		{"a", false},
		{"a1", false},
		{"a_b_c", false},
		{"PARAKEET", true},              // uppercase rejected
		{"-leading", true},              // must start with [a-z0-9]
		{"with space", true},            // no spaces
		{"with.dot", true},              // no dots
		{strings.Repeat("a", 33), true}, // too long
		{"", true},                      // empty
		{"payload", true},               // reserved
		{"format", true},                // reserved
		{"audio", true},                 // reserved
		{"meeting", true},               // reserved
		{"transcript", true},            // reserved
		{"provenance", true},            // reserved
	}
	for _, tc := range cases {
		err := ValidateTranscriptID(tc.id)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateTranscriptID(%q): expected error, got nil", tc.id)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateTranscriptID(%q): unexpected error %v", tc.id, err)
		}
	}
}

func TestTranscriptIDToTagPrefix(t *testing.T) {
	cases := map[string]string{
		"parakeet":      "CASSINI_TX_PARAKEET_PAYLOAD_",
		"canary":        "CASSINI_TX_CANARY_PAYLOAD_",
		"raw-asr":       "CASSINI_TX_RAW_ASR_PAYLOAD_",
		"readable-qwen": "CASSINI_TX_READABLE_QWEN_PAYLOAD_",
		"a1_b":          "CASSINI_TX_A1_B_PAYLOAD_",
	}
	for id, want := range cases {
		if got := TranscriptIDToTagPrefix(id); got != want {
			t.Errorf("TranscriptIDToTagPrefix(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestEncodeTranscriptBodyRoundTrip(t *testing.T) {
	body := sampleBody("spk_0", "hello", "world")
	payload, ref, err := EncodeTranscriptBody(body, "parakeet", RoleRawASR, 0)
	if err != nil {
		t.Fatalf("EncodeTranscriptBody: %v", err)
	}
	if ref.Prefix != "CASSINI_TX_PARAKEET_PAYLOAD_" {
		t.Errorf("Prefix = %q", ref.Prefix)
	}
	if ref.ChunkCount != len(payload.Chunks) {
		t.Errorf("ChunkCount %d != len(Chunks) %d", ref.ChunkCount, len(payload.Chunks))
	}
	if ref.SHA256 != payload.SHA256 {
		t.Errorf("ref.SHA256 != payload.SHA256")
	}
	if ref.MIME != TranscriptBodyMIMEWords {
		t.Errorf("MIME = %q, want %q", ref.MIME, TranscriptBodyMIMEWords)
	}

	// Round-trip: reassemble chunks, base64url-decode, gzip-decompress, parse.
	encoded := strings.Join(payload.Chunks, "")
	compressed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	gzr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	raw, err := io_ReadAll(gzr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	var decoded TranscriptBody
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if decoded.WordCount != 2 || len(decoded.Items) != 2 || decoded.Items[1].Text != "world" {
		t.Errorf("decoded body wrong: %+v", decoded)
	}
}

// Inline ReadAll to avoid pulling io into the test imports when other tests
// don't need it.
func io_ReadAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				return out.Bytes(), nil
			}
			return out.Bytes(), err
		}
	}
}

func TestEncodeManifestV2EmitsExpectedShape(t *testing.T) {
	manifest := baseManifestV2()
	transcripts := []TranscriptInput{
		{
			ID:      "parakeet",
			Role:    RoleRawASR,
			Default: false,
			Body:    sampleBody("spk_0", "hello", "from", "parakeet"),
			Provenance: &ProcessingStep{
				Backend: "sherpa-onnx-go",
				Engine:  "sherpa-onnx",
				Model:   "parakeet-tdt-0.6b-v2-int8",
			},
		},
		{
			ID:      "canary",
			Role:    RoleRawASR,
			Default: true,
			Body:    sampleBody("spk_0", "hello", "from", "canary", "model"),
			Provenance: &ProcessingStep{
				Backend: "sherpa-onnx-go",
				Engine:  "sherpa-onnx",
				Model:   "canary-1b-v2",
			},
		},
	}
	encoded, err := EncodeManifestV2(manifest, transcripts, 0)
	if err != nil {
		t.Fatalf("EncodeManifestV2: %v", err)
	}
	if len(encoded.Transcripts) != 2 {
		t.Fatalf("expected 2 transcripts encoded, got %d", len(encoded.Transcripts))
	}

	// Decode the main manifest and check structure.
	compressed, err := base64.RawURLEncoding.DecodeString(strings.Join(encoded.Main.Chunks, ""))
	if err != nil {
		t.Fatalf("base64 decode main: %v", err)
	}
	gzr, _ := gzip.NewReader(bytes.NewReader(compressed))
	raw, _ := io_ReadAll(gzr)
	var wire manifestV2Wire
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal main manifest: %v", err)
	}
	if wire.Version != 2 {
		t.Errorf("version = %d, want 2", wire.Version)
	}
	if wire.Kind != "cassini-portable-meeting" {
		t.Errorf("kind = %q", wire.Kind)
	}
	if len(wire.Transcripts) != 2 {
		t.Errorf("transcripts in wire = %d", len(wire.Transcripts))
	}
	// Order should mirror input order; the default flag should travel.
	if !wire.Transcripts[1].Default || wire.Transcripts[0].Default {
		t.Errorf("default flags wrong: %+v", wire.Transcripts)
	}
	// Provenance keyed by transcript id
	if wire.Provenance == nil || wire.Provenance.SpeechToText == nil {
		t.Fatalf("provenance.speechToText missing")
	}
	if wire.Provenance.SpeechToText["canary"] == nil || wire.Provenance.SpeechToText["canary"].Model != "canary-1b-v2" {
		t.Errorf("canary provenance wrong: %+v", wire.Provenance.SpeechToText["canary"])
	}
}

func TestEncodeManifestV3UsesCompressedOpusIntegrity(t *testing.T) {
	manifest := baseManifestV3()
	transcripts := []TranscriptInput{{
		ID: "parakeet", Role: RoleRawASR, Default: true,
		Body: sampleBody("spk_0", "compressed", "identity"),
	}}
	encoded, err := EncodeManifestV3(manifest, transcripts, 0)
	if err != nil {
		t.Fatalf("EncodeManifestV3: %v", err)
	}

	var wire manifestV2Wire
	if err := json.Unmarshal(encoded.Main.JSON, &wire); err != nil {
		t.Fatalf("decode v3 wire: %v", err)
	}
	if wire.Version != 3 {
		t.Errorf("version = %d, want 3", wire.Version)
	}
	if wire.Integrity.MatchPolicy != AudioMatchPolicy {
		t.Errorf("matchPolicy = %q, want %q", wire.Integrity.MatchPolicy, AudioMatchPolicy)
	}
	if wire.Integrity.OpusSHA256 != strings.Repeat("b", 64) {
		t.Errorf("opusAudioSha256 = %q", wire.Integrity.OpusSHA256)
	}
	if wire.Integrity.PCMSHA256 != "" || wire.Integrity.PCMFormat != "" {
		t.Errorf("v3 leaked legacy PCM integrity: %+v", wire.Integrity)
	}

	tags := BuildOpusTagsV3(manifest, encoded, "parakeet")
	if tags["CASSINI_FORMAT"] != FormatV3 {
		t.Errorf("CASSINI_FORMAT = %q, want %q", tags["CASSINI_FORMAT"], FormatV3)
	}
	if tags["CASSINI_AUDIO_OPUS_SHA256"] != strings.Repeat("b", 64) {
		t.Errorf("CASSINI_AUDIO_OPUS_SHA256 = %q", tags["CASSINI_AUDIO_OPUS_SHA256"])
	}
	if _, ok := tags["CASSINI_AUDIO_PCM_SHA256"]; ok {
		t.Error("v3 emitted CASSINI_AUDIO_PCM_SHA256")
	}
}

func TestEncodeManifestV3RequiresCompressedDigest(t *testing.T) {
	manifest := baseManifestV3()
	manifest.Integrity.OpusSHA256 = ""
	_, err := EncodeManifestV3(manifest, []TranscriptInput{{
		ID: "parakeet", Role: RoleRawASR, Default: true, Body: sampleBody("spk_0", "x"),
	}}, 0)
	if err == nil || !strings.Contains(err.Error(), "opusAudioSha256") {
		t.Fatalf("error = %v, want missing opusAudioSha256", err)
	}
}

func TestEncodeManifestV2RejectsBadInputs(t *testing.T) {
	manifest := baseManifestV2()
	body := sampleBody("spk_0", "x")

	cases := []struct {
		name        string
		transcripts []TranscriptInput
		expectMsg   string
	}{
		{
			name:        "empty list",
			transcripts: nil,
			expectMsg:   "at least one transcript",
		},
		{
			name: "reserved id",
			transcripts: []TranscriptInput{
				{ID: "payload", Role: RoleRawASR, Body: body},
			},
			expectMsg: "reserved",
		},
		{
			name: "bad id pattern",
			transcripts: []TranscriptInput{
				{ID: "Parakeet", Role: RoleRawASR, Body: body},
			},
			expectMsg: "does not match",
		},
		{
			name: "duplicate id",
			transcripts: []TranscriptInput{
				{ID: "parakeet", Role: RoleRawASR, Body: body, Default: true},
				{ID: "parakeet", Role: RoleRawASR, Body: body},
			},
			expectMsg: "duplicate",
		},
		{
			name: "two defaults raw-asr",
			transcripts: []TranscriptInput{
				{ID: "parakeet", Role: RoleRawASR, Body: body, Default: true},
				{ID: "canary", Role: RoleRawASR, Body: body, Default: true},
			},
			expectMsg: "more than one default raw-ASR",
		},
		{
			name: "readable-cleanup without source",
			transcripts: []TranscriptInput{
				{ID: "qwen", Role: RoleReadableCleanup, Body: body},
			},
			expectMsg: "sourceTranscriptId",
		},
		{
			name: "unknown source id",
			transcripts: []TranscriptInput{
				{ID: "parakeet", Role: RoleRawASR, Body: body, Default: true},
				{ID: "qwen", Role: RoleReadableCleanup, Body: body, SourceTranscriptID: "nonexistent"},
			},
			expectMsg: "not in this file",
		},
		{
			name: "unknown role",
			transcripts: []TranscriptInput{
				{ID: "weird", Role: "alien", Body: body},
			},
			expectMsg: "unknown role",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeManifestV2(manifest, tc.transcripts, 0)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.expectMsg)
			}
			if !strings.Contains(err.Error(), tc.expectMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.expectMsg)
			}
		})
	}
}

func TestBuildOpusTagsV2EmitsPerTranscriptDescriptorsAndChunks(t *testing.T) {
	manifest := baseManifestV2()
	manifest.Meeting.RecordedAtLocal = "2026-05-12T12:00:00"
	transcripts := []TranscriptInput{
		{
			ID: "parakeet", Role: RoleRawASR, Default: false,
			Body: sampleBody("spk_0", "alpha", "bravo"),
		},
		{
			ID: "canary", Role: RoleRawASR, Default: true,
			Body: sampleBody("spk_0", "alpha", "bravo", "charlie"),
		},
		{
			ID: "readable-qwen", Role: RoleReadableCleanup, Default: true,
			SourceTranscriptID: "canary",
			Body: TranscriptBody{
				Format: "cassini.readable.v1", WordCount: 2,
				Items: []TranscriptItem{{Speaker: "spk_0", StartMS: 0, EndMS: 800, Text: "Alpha bravo charlie."}},
			},
		},
	}
	encoded, err := EncodeManifestV2(manifest, transcripts, 0)
	if err != nil {
		t.Fatalf("EncodeManifestV2: %v", err)
	}
	tags := BuildOpusTagsV2(manifest, encoded, "canary")

	mustHave := []string{
		"CASSINI_FORMAT",
		"CASSINI_PAYLOAD_CHUNK_COUNT",
		"CASSINI_PAYLOAD_SHA256",
		"CASSINI_TRANSCRIPT_IDS",
		"CASSINI_TRANSCRIPT_DEFAULT",
		"CASSINI_TX_PARAKEET_PAYLOAD_CHUNK_COUNT",
		"CASSINI_TX_PARAKEET_PAYLOAD_SHA256",
		"CASSINI_TX_PARAKEET_PAYLOAD_MIME",
		"CASSINI_TX_PARAKEET_PAYLOAD_ENCODING",
		"CASSINI_TX_CANARY_PAYLOAD_CHUNK_COUNT",
		"CASSINI_TX_CANARY_PAYLOAD_MIME",
		"CASSINI_TX_READABLE_QWEN_PAYLOAD_CHUNK_COUNT",
		"CASSINI_TX_READABLE_QWEN_PAYLOAD_MIME",
		"CASSINI_RECORDED_AT_LOCAL",
		"CASSINI_DECODE_HINT",
	}
	for _, key := range mustHave {
		if _, ok := tags[key]; !ok {
			t.Errorf("missing tag %q", key)
		}
	}
	if got := tags["CASSINI_FORMAT"]; got != FormatV2 {
		t.Errorf("CASSINI_FORMAT = %q, want %q", got, FormatV2)
	}
	if got := tags["CASSINI_TRANSCRIPT_DEFAULT"]; got != "canary" {
		t.Errorf("CASSINI_TRANSCRIPT_DEFAULT = %q", got)
	}
	if got := tags["CASSINI_TRANSCRIPT_IDS"]; got != "canary,parakeet,readable-qwen" {
		t.Errorf("CASSINI_TRANSCRIPT_IDS = %q", got)
	}
	if got := tags["CASSINI_TX_PARAKEET_PAYLOAD_MIME"]; got != TranscriptBodyMIMEWords {
		t.Errorf("parakeet MIME = %q", got)
	}
	if got := tags["CASSINI_TX_READABLE_QWEN_PAYLOAD_MIME"]; got != TranscriptBodyMIMEReadable {
		t.Errorf("readable MIME = %q", got)
	}

	// Per-transcript chunk presence: chunk 0 must exist for each.
	for _, prefix := range []string{"CASSINI_TX_PARAKEET_PAYLOAD_", "CASSINI_TX_CANARY_PAYLOAD_", "CASSINI_TX_READABLE_QWEN_PAYLOAD_"} {
		if _, ok := tags[prefix+"000"]; !ok {
			t.Errorf("missing first chunk for %q", prefix)
		}
	}
}

func TestBuildOpusTagsV2AndWireCarryTheRoom(t *testing.T) {
	manifest := baseManifestV2()
	manifest.Meeting.RoomID = "a7bc3k9x"
	manifest.Meeting.RoomName = "Weekly Sync"
	transcripts := []TranscriptInput{
		{ID: "canary", Role: RoleRawASR, Default: true, Body: sampleBody("spk_0", "alpha")},
	}
	encoded, err := EncodeManifestV2(manifest, transcripts, 0)
	if err != nil {
		t.Fatalf("EncodeManifestV2: %v", err)
	}

	tags := BuildOpusTagsV2(manifest, encoded, "canary")
	if got := tags["CASSINI_ROOM_ID"]; got != "a7bc3k9x" {
		t.Errorf("CASSINI_ROOM_ID = %q, want %q", got, "a7bc3k9x")
	}
	if got := tags["CASSINI_ROOM_NAME"]; got != "Weekly Sync" {
		t.Errorf("CASSINI_ROOM_NAME = %q, want %q", got, "Weekly Sync")
	}

	// v2 is the format the operator actually emits, so the room has to survive
	// into the wire manifest too — the plain tags are a convenience, not the
	// contract the viewer and the exporter read.
	var wire struct {
		Meeting struct {
			RoomID   string `json:"roomId"`
			RoomName string `json:"roomName"`
		} `json:"meeting"`
	}
	if err := json.Unmarshal(encoded.Main.JSON, &wire); err != nil {
		t.Fatalf("decode v2 wire manifest: %v", err)
	}
	if wire.Meeting.RoomID != "a7bc3k9x" || wire.Meeting.RoomName != "Weekly Sync" {
		t.Errorf("wire meeting room = %q/%q, want %q/%q",
			wire.Meeting.RoomID, wire.Meeting.RoomName, "a7bc3k9x", "Weekly Sync")
	}

	// No room: no tags, and no empty keys in the wire manifest either.
	plainTags := BuildOpusTagsV2(baseManifestV2(), encoded, "canary")
	for _, key := range []string{"CASSINI_ROOM_ID", "CASSINI_ROOM_NAME"} {
		if _, ok := plainTags[key]; ok {
			t.Errorf("%s is present on a meeting with no room, want absent", key)
		}
	}
}

func TestBuildOpusTagsV2AndWireCarryTheProvenance(t *testing.T) {
	manifest := baseManifestV2()
	manifest.Meeting.JobID = "01K3Q7W8ZC9F0MJXQ2NB8V4RTD"
	manifest.Meeting.AttemptNumber = 2
	transcripts := []TranscriptInput{
		{ID: "canary", Role: RoleRawASR, Default: true, Body: sampleBody("spk_0", "alpha")},
	}
	encoded, err := EncodeManifestV2(manifest, transcripts, 0)
	if err != nil {
		t.Fatalf("EncodeManifestV2: %v", err)
	}

	tags := BuildOpusTagsV2(manifest, encoded, "canary")
	if got := tags["CASSINI_JOB_ID"]; got != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" {
		t.Errorf("CASSINI_JOB_ID = %q, want the job id", got)
	}
	if got := tags["CASSINI_ATTEMPT_NUMBER"]; got != "2" {
		t.Errorf("CASSINI_ATTEMPT_NUMBER = %q, want %q", got, "2")
	}

	// v2 is the format the operator emits, so — as with the room — the plain
	// tags are the convenience and the wire manifest is the contract.
	var wire struct {
		Meeting struct {
			JobID         string `json:"jobId"`
			AttemptNumber int    `json:"attemptNumber"`
		} `json:"meeting"`
	}
	if err := json.Unmarshal(encoded.Main.JSON, &wire); err != nil {
		t.Fatalf("decode v2 wire manifest: %v", err)
	}
	if wire.Meeting.JobID != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" || wire.Meeting.AttemptNumber != 2 {
		t.Errorf("wire meeting provenance = %q/%d, want the job id and attempt 2",
			wire.Meeting.JobID, wire.Meeting.AttemptNumber)
	}

	plainTags := BuildOpusTagsV2(baseManifestV2(), encoded, "canary")
	for _, key := range []string{"CASSINI_JOB_ID", "CASSINI_ATTEMPT_NUMBER"} {
		if _, ok := plainTags[key]; ok {
			t.Errorf("%s is present on a meeting with no operator lineage, want absent", key)
		}
	}
}
