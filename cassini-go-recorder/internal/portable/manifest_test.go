package portable

import (
	"encoding/json"
	"strings"
	"testing"
)

func publishedManifestFixture(meeting Meeting) Manifest {
	meeting.ID = "mtg_" + strings.Repeat("a", 64)
	meeting.CreatedAtUTC = "2026-03-13T10:00:00Z"
	meeting.DurationMS = 1000
	return NormalizePublishedManifest(Manifest{
		Meeting: meeting,
		Audio: Audio{
			Container: "ogg", Codec: "opus", SampleRate: 48000,
			Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Integrity: Integrity{
			MatchPolicy: AudioMatchPolicy,
			OpusSHA256:  strings.Repeat("b", 64),
			SampleRate:  48000, Channels: 1, SampleCount: 48000, DurationMS: 1000,
		},
		Speakers: []Speaker{{ID: "spk_1", Label: "Speaker 1"}},
	})
}

func encodePublishedFixture(t *testing.T, manifest Manifest) EncodedMultiTranscriptManifest {
	t.Helper()
	var provenance *ProcessingStep
	if manifest.Provenance != nil {
		provenance = manifest.Provenance.SpeechToText
	}
	encoded, err := EncodePublishedManifest(manifest, []TranscriptInput{{
		ID: RoleRawASR, Role: RoleRawASR, Default: true,
		Body: TranscriptBody{
			Format: "cassini.words.v1", WordCount: 1,
			Items: []TranscriptItem{{Speaker: "spk_1", StartMS: 0, EndMS: 100, Text: "hello"}},
		},
		Provenance: provenance,
	}}, 0)
	if err != nil {
		t.Fatalf("EncodePublishedManifest: %v", err)
	}
	return encoded
}

func publishedFixtureTags(t *testing.T, manifest Manifest) map[string]string {
	t.Helper()
	return BuildPublishedOpusTags(manifest, encodePublishedFixture(t, manifest), RoleRawASR)
}

func TestBuildPublishedOpusTagsIncludesProcessingProvenance(t *testing.T) {
	manifest := publishedManifestFixture(Meeting{
		Title: "Weekly Sync", RecordedAtLocal: "2026-03-13T11:00:00",
		ProcessedAtUTC: "2026-03-13T10:02:00Z",
	})
	manifest.Provenance = &Provenance{
		SpeechToText: &ProcessingStep{
			Backend: "local-asr", Engine: "asr-engine", Model: "meeting-model",
			Device: "cpu", Language: "en",
		},
	}
	tags := publishedFixtureTags(t, manifest)

	checks := map[string]string{
		"CASSINI_FORMAT":            Format,
		"CASSINI_STT_ENGINE":        "asr-engine",
		"CASSINI_STT_MODEL":         "meeting-model",
		"CASSINI_RECORDED_AT_LOCAL": "2026-03-13T11:00:00",
		"CASSINI_PROCESSED_AT":      "2026-03-13T10:02:00Z",
	}
	for key, want := range checks {
		if got := tags[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestBuildPublishedOpusTagsEmitsOptionalIdentityTags(t *testing.T) {
	withIdentity := publishedFixtureTags(t, publishedManifestFixture(Meeting{
		Title: "Weekly Sync", RoomID: "rm_0123456789abcdef",
		JobID: "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", AttemptNumber: 2,
	}))
	checks := map[string]string{
		"CASSINI_ROOM_ID": "rm_0123456789abcdef",
		"CASSINI_JOB_ID":  "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "CASSINI_ATTEMPT_NUMBER": "2",
	}
	for key, want := range checks {
		if got := withIdentity[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	withoutIdentity := publishedFixtureTags(t, publishedManifestFixture(Meeting{Title: "Some File"}))
	for _, key := range []string{"CASSINI_ROOM_ID", "CASSINI_JOB_ID", "CASSINI_ATTEMPT_NUMBER"} {
		if _, ok := withoutIdentity[key]; ok {
			t.Errorf("%s is present without a value", key)
		}
	}
}

func TestPublishedManifestIdentitySurvivesEncoding(t *testing.T) {
	manifest := publishedManifestFixture(Meeting{
		Title: "Weekly Sync", RoomID: "rm_0123456789abcdef",
		JobID: "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", AttemptNumber: 3,
	})
	encoded := encodePublishedFixture(t, manifest)

	var decoded Manifest
	if err := json.Unmarshal(encoded.Main.JSON, &decoded); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if err := ValidatePublishedManifest(decoded); err != nil {
		t.Fatalf("ValidatePublishedManifest: %v", err)
	}
	if decoded.Meeting.RoomID != manifest.Meeting.RoomID {
		t.Errorf("meeting room id = %q", decoded.Meeting.RoomID)
	}
	if decoded.Meeting.JobID != manifest.Meeting.JobID || decoded.Meeting.AttemptNumber != 3 {
		t.Errorf("meeting lineage = %q/%d", decoded.Meeting.JobID, decoded.Meeting.AttemptNumber)
	}
}

func TestValidatePublishedManifestRejectsUnsupportedShapes(t *testing.T) {
	valid := publishedManifestFixture(Meeting{Title: "Meeting"})
	encoded := encodePublishedFixture(t, valid)
	if err := json.Unmarshal(encoded.Main.JSON, &valid); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	tests := []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{name: "version", edit: func(m *Manifest) { m.Version++ }, want: "unsupported payload version"},
		{name: "profile", edit: func(m *Manifest) { m.Profile = "other" }, want: "unsupported payload profile"},
		{name: "transcript index", edit: func(m *Manifest) { m.Transcripts = nil }, want: "transcripts must contain"},
		{name: "integrity policy", edit: func(m *Manifest) { m.Integrity.MatchPolicy = "other" }, want: "unsupported audio integrity"},
		{name: "integrity digest", edit: func(m *Manifest) { m.Integrity.OpusSHA256 = "bad" }, want: "opusAudioSha256"},
		{name: "transcript role", edit: func(m *Manifest) { m.Transcripts[0].Role = "other" }, want: "unsupported role"},
		{name: "transcript encoding", edit: func(m *Manifest) { m.Transcripts[0].PayloadRef.Encoding = "other" }, want: "unsupported payload encoding"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := valid
			manifest.Transcripts = append([]TranscriptEntry(nil), valid.Transcripts...)
			manifest.ReadableTranscripts = append([]TranscriptEntry(nil), valid.ReadableTranscripts...)
			tc.edit(&manifest)
			if err := ValidatePublishedManifest(manifest); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want message containing %q", err, tc.want)
			}
		})
	}
}

// A file written before the LLM cleanup step was withdrawn may still carry a
// readable-cleanup entry. Such a file must stay readable: the entry is skipped,
// not rejected. Failing here would turn an older meeting into an error and take
// its perfectly good raw transcript down with it, which is a worse outcome than
// the field we removed.
func TestValidatePublishedManifestSkipsWithdrawnReadableCleanupEntries(t *testing.T) {
	manifest := publishedManifestFixture(Meeting{
		Title: "Weekly Sync", RecordedAtLocal: "2026-03-13T11:00:00",
	})
	// Round-trip through the real producer so the raw entry is exactly what a
	// published file carries, then graft on the shape an older producer wrote.
	var wire Manifest
	if err := json.Unmarshal(encodePublishedFixture(t, manifest).Main.JSON, &wire); err != nil {
		t.Fatalf("decode encoded manifest: %v", err)
	}
	if len(wire.Transcripts) == 0 {
		t.Fatal("fixture produced no raw transcript entry")
	}
	wire.ReadableTranscripts = []TranscriptEntry{{
		ID: "readable", Role: RoleWithdrawnReadableCleanup, Format: "transcript.readable.v1",
		SourceTranscriptID: wire.Transcripts[0].ID,
		PayloadRef:         wire.Transcripts[0].PayloadRef,
	}}

	if err := ValidatePublishedManifest(wire); err != nil {
		t.Fatalf("a withdrawn readable-cleanup entry must be skipped, not rejected: %v", err)
	}
}

// The role is withdrawn for producers, not merely renamed: nothing may write a
// new one. Keeping the constant is only about recognising old files.
func TestPackTranscriptsRefusesToWriteAWithdrawnReadableCleanupEntry(t *testing.T) {
	body := TranscriptBody{
		Format: "cassini.words.v1", WordCount: 1,
		Items: []TranscriptItem{{Speaker: "spk_0", StartMS: 0, EndMS: 10, Text: "hi"}},
	}
	_, err := EncodePublishedManifest(publishedManifestFixture(Meeting{Title: "x"}), []TranscriptInput{
		{ID: "parakeet", Role: RoleRawASR, Default: true, Body: body},
		{ID: "old", Role: RoleWithdrawnReadableCleanup, SourceTranscriptID: "parakeet", Body: body},
	}, DefaultPayloadChunkSize)
	if err == nil {
		t.Fatal("packing a readable-cleanup entry must fail: nothing produces them any more")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("error should name the unknown role, got %v", err)
	}
}
