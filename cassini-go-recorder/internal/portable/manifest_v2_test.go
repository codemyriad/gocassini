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
			MatchPolicy: AudioMatchPolicy,
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
		{"PARAKEET", true},      // uppercase rejected
		{"-leading", true},      // must start with [a-z0-9]
		{"with space", true},    // no spaces
		{"with.dot", true},      // no dots
		{strings.Repeat("a", 33), true}, // too long
		{"", true},              // empty
		{"payload", true},       // reserved
		{"format", true},        // reserved
		{"audio", true},         // reserved
		{"meeting", true},       // reserved
		{"transcript", true},    // reserved
		{"provenance", true},    // reserved
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
