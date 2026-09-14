package cassini

import (
	"encoding/json"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// publishedPortableTags builds the OpusTags of a published meeting the same
// way pack does, without touching ffmpeg, so a reader test can edit one tag
// and see what decodePortableMeetingPayload makes of it.
func publishedPortableTags(t *testing.T) map[string]string {
	t.Helper()
	manifest := portable.NormalizePublishedManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID: "mtg_" + strings.Repeat("c", 64), Title: "Weekly Sync",
			CreatedAtUTC: "2026-03-11T08:30:00Z", DurationMS: 200,
		},
		Audio: portable.Audio{
			Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1, SampleCount: 9600, DurationMS: 200,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: strings.Repeat("a", 64),
			SampleRate: 48000, Channels: 1, SampleCount: 9600, DurationMS: 200,
		},
		Speakers: []portable.Speaker{{ID: "spk1", Label: "Silvio"}},
	})
	inputs := []portable.TranscriptInput{{
		ID: portable.DefaultWordsTranscriptID, Default: true, Language: "en",
		Body: portable.TranscriptBody{
			Format: "cassini.words.v1", Language: "en", WordCount: 1,
			Items: []portable.TranscriptItem{{Speaker: "spk1", StartMS: 0, EndMS: 80, Text: "Hello"}},
		},
	}}
	encoded, err := portable.EncodePublishedManifest(manifest, inputs, portable.DefaultPayloadChunkSize)
	if err != nil {
		t.Fatalf("encode published manifest: %v", err)
	}
	return portable.BuildPublishedOpusTags(manifest, encoded, portable.DefaultWordsTranscriptID)
}

// Every file published before the schema moved to format.gocassini.com carries
// the codemyriad.io identifier. It is the same version 1 format, so it must
// read; a schema nobody published must still be refused by name.
func TestDecodePortableMeetingPayloadAcceptsThePreMoveSchemaIdentifier(t *testing.T) {
	for _, schema := range append([]string{portable.PayloadSchema}, portable.LegacyPayloadSchemas...) {
		t.Run(schema, func(t *testing.T) {
			tags := publishedPortableTags(t)
			tags["CASSINI_PAYLOAD_SCHEMA"] = schema
			rawJSON, err := decodePortableMeetingPayload(tags)
			if err != nil {
				t.Fatalf("decodePortableMeetingPayload: %v", err)
			}
			var manifest portable.Manifest
			if err := json.Unmarshal(rawJSON, &manifest); err != nil {
				t.Fatalf("parse decoded manifest: %v", err)
			}
			if manifest.Meeting.Title != "Weekly Sync" {
				t.Errorf("decoded title = %q, want %q", manifest.Meeting.Title, "Weekly Sync")
			}
		})
	}

	tags := publishedPortableTags(t)
	tags["CASSINI_PAYLOAD_SCHEMA"] = "https://example.test/schema/cassini-portable-meeting-manifest-v1.schema.json"
	if _, err := decodePortableMeetingPayload(tags); err == nil || !strings.Contains(err.Error(), "unsupported CASSINI_PAYLOAD_SCHEMA") {
		t.Fatalf("error = %v, want an unsupported CASSINI_PAYLOAD_SCHEMA error", err)
	}
}
