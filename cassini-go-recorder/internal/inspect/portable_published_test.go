package inspect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// The published format draws one line through the middle of this file: a body
// that will not decode is that transcript's problem, and a tag that appears
// twice is the whole file's. These tests hold that line.

func TestInspectRepeatedBodyChunkShowsNoManifestField(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createPortableDraft2OpusFixtureWith(t, filepath.Join(tmp, "repeated.opus"), portableDraft2FixtureOptions{
		words:                      []string{"one", "two", "three"},
		repeatFirstTranscriptChunk: true,
	})
	var out bytes.Buffer
	err := InspectPath(&out, path)
	if err == nil {
		t.Fatalf("a repeated load-bearing tag reported success: %q", out.String())
	}
	got := out.String()
	if !strings.Contains(got, "cassini=invalid-cassini-metadata") {
		t.Errorf("expected cassini=invalid-cassini-metadata, got %q", got)
	}
	// "The consumer MUST NOT show any manifest field."
	for _, leaked := range []string{"Lantern Festival", "meeting-v2", "transcript id=", "payload encoding="} {
		if strings.Contains(got, leaked) {
			t.Errorf("a manifest field survived an invalid file: %q in %q", leaked, got)
		}
	}
}

func TestInspectMissingBodyChunkKeepsTheFileState(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createPortableDraft2OpusFixtureWith(t, filepath.Join(tmp, "holed.opus"), portableDraft2FixtureOptions{
		words:                   []string{"one", "two", "three"},
		dropLastTranscriptChunk: true,
	})
	var out bytes.Buffer
	if err := InspectPath(&out, path); err == nil {
		t.Fatalf("an unreadable body reported success: %q", out.String())
	}
	got := out.String()
	if strings.Contains(got, "cassini=invalid-cassini-metadata") {
		t.Errorf("an unreadable body is that transcript's problem, not the file's; got %q", got)
	}
	// The meeting is still good, and the reader still says what it lost.
	if !strings.Contains(got, "Lantern Festival") {
		t.Errorf("expected the meeting to survive one unreadable body, got %q", got)
	}
	if !strings.Contains(got, "warning=transcript raw-asr body could not be read") {
		t.Errorf("expected a warning naming the transcript, got %q", got)
	}
}

// decodeTranscriptBody must believe payloadRef, not the descriptor tags that
// copy it: "the entry's payloadRef is the record for all seven values".
func TestDecodeTranscriptBodyTrustsPayloadRefOverTags(t *testing.T) {
	body := portable.TranscriptBody{
		Format:    "cassini.words.v1",
		Language:  "en",
		WordCount: 2,
		Items: []portable.TranscriptItem{
			{Speaker: "spk1", StartMS: 0, EndMS: 80, Text: "one"},
			{Speaker: "spk1", StartMS: 100, EndMS: 180, Text: "two"},
		},
	}
	input := portable.TranscriptInput{ID: "raw-asr", Role: portable.RoleRawASR, Default: true, Body: body}
	encoded, err := portable.EncodeDraft2Manifest(portable.NormalizeDraft1Manifest(portable.Manifest{
		Meeting:  portable.Meeting{ID: "m", Title: "t", CreatedAtUTC: "2026-09-02T00:00:00Z"},
		Speakers: []portable.Speaker{{ID: "spk1", Label: "Silvio"}},
	}), []portable.TranscriptInput{input}, 4096)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	named := encoded.Transcripts[0]
	entry := portable.TranscriptEntry{
		ID:      named.ID,
		Role:    portable.RoleRawASR,
		Default: true,
		Format:  "cassini.words.v1",
		PayloadRef: portable.PayloadRef{
			Prefix:     named.Prefix,
			ChunkCount: len(named.Payload.Chunks),
			SHA256:     named.Payload.SHA256,
			RawBytes:   named.Payload.RawBytes,
			GzipBytes:  named.Payload.CompressedBytes,
			Encoding:   portable.PayloadEncoding,
		},
	}
	tags := map[string]string{}
	for i, chunk := range named.Payload.Chunks {
		tags[fmt.Sprintf("%s%03d", entry.PayloadRef.Prefix, i)] = chunk
	}
	tags[entry.PayloadRef.Prefix+"CHUNK_COUNT"] = fmt.Sprint(entry.PayloadRef.ChunkCount)
	tags[entry.PayloadRef.Prefix+"SHA256"] = entry.PayloadRef.SHA256
	tags[entry.PayloadRef.Prefix+"RAW_BYTES"] = fmt.Sprint(entry.PayloadRef.RawBytes)

	t.Run("a wrong tag is a warning, not a failure", func(t *testing.T) {
		poisoned := map[string]string{}
		for k, v := range tags {
			poisoned[k] = v
		}
		poisoned[entry.PayloadRef.Prefix+"SHA256"] = strings.Repeat("f", 64)
		got, warnings, err := decodeTranscriptBody(poisoned, entry)
		if err != nil {
			t.Fatalf("a body that matches payloadRef was rejected over a tag: %v", err)
		}
		if len(got.Items) != 2 {
			t.Errorf("Items = %d, want 2", len(got.Items))
		}
		if len(warnings) == 0 {
			t.Errorf("expected a warning naming the tag that disagrees")
		}
	})

	t.Run("a wrong payloadRef is a failure", func(t *testing.T) {
		bad := entry
		bad.PayloadRef.SHA256 = strings.Repeat("a", 64)
		if _, _, err := decodeTranscriptBody(tags, bad); err == nil {
			t.Errorf("a body contradicting payloadRef.sha256 was accepted")
		}
	})

	t.Run("the prefix is used as written", func(t *testing.T) {
		moved := entry
		moved.PayloadRef.Prefix = "CASSINI_TX_ELSEWHERE_PAYLOAD_"
		relocated := map[string]string{}
		for k, v := range tags {
			relocated[strings.Replace(k, entry.PayloadRef.Prefix, moved.PayloadRef.Prefix, 1)] = v
		}
		if _, _, err := decodeTranscriptBody(relocated, moved); err != nil {
			t.Errorf("payloadRef.prefix was re-derived rather than used: %v", err)
		}
	})
}

// "wordCount ... A convenience; items wins on disagreement."
func TestBodyWordCountDisagreementUsesItemsLength(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := createPortableDraft2OpusFixture(t, filepath.Join(tmp, "counted.opus"), []string{"one", "two", "three"})
	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got := out.String(); !strings.Contains(got, " words=3 ") {
		t.Errorf("expected words=3 read out of the chunk set, got %q", got)
	}
}

// One segment for every speaker turn: the export used to flatten a
// multi-speaker transcript into one speakerless run.
func TestTranscriptJSONPreservesSpeakerTurns(t *testing.T) {
	extracted := ExtractedTranscript{
		Language:  "en",
		WordCount: 4,
		Words: []TranscriptWord{
			{Speaker: "spk1", Text: "morning", StartMS: 0, EndMS: 50},
			{Speaker: "spk1", Text: "all", StartMS: 50, EndMS: 90},
			{Speaker: "spk2", Text: "hello", StartMS: 90, EndMS: 140},
			{Speaker: "spk1", Text: "right", StartMS: 140, EndMS: 200},
		},
	}
	var rendered bytes.Buffer
	if err := WriteTranscriptWordsV1JSON(&rendered, extracted); err != nil {
		t.Fatalf("WriteTranscriptWordsV1JSON: %v", err)
	}
	var doc struct {
		WordCount int `json:"wordCount"`
		Segments  []struct {
			Speaker string `json:"speaker"`
			Words   []struct {
				Text string `json:"text"`
			} `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(rendered.Bytes(), &doc); err != nil {
		t.Fatalf("parse rendered JSON: %v", err)
	}
	if len(doc.Segments) != 3 {
		t.Fatalf("Segments = %d, want 3 turns", len(doc.Segments))
	}
	want := []struct {
		speaker string
		words   int
	}{{"spk1", 2}, {"spk2", 1}, {"spk1", 1}}
	for i, w := range want {
		if doc.Segments[i].Speaker != w.speaker || len(doc.Segments[i].Words) != w.words {
			t.Errorf("segment %d = %q/%d words, want %q/%d",
				i, doc.Segments[i].Speaker, len(doc.Segments[i].Words), w.speaker, w.words)
		}
	}
	if doc.WordCount != 4 {
		t.Errorf("wordCount = %d, want 4", doc.WordCount)
	}
}
