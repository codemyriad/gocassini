package inspect

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

func TestInspectPathPlainOpusFallsBackToPlainAudio(t *testing.T) {
	requireFFMediaTools(t)
	path := createTestOpus(t, filepath.Join(t.TempDir(), "plain.opus"))

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect plain opus: %v", err)
	}
	if !strings.Contains(out.String(), "cassini=plain-audio") {
		t.Fatalf("expected plain-audio fallback, got %q", out.String())
	}
}

func TestInspectPathPublishedMeetingReadsChunkedTranscript(t *testing.T) {
	requireFFMediaTools(t)
	words := []string{"Hello", "team", "lantern", "festival", "tonight"}
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "meeting.opus"), portableFixtureOptions{words: words})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect portable opus: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"portable_meeting=" + path,
		" words=5 ",
		"cassini=ok",
		"opus_sha256=",
		"payload encoding=base64url+gzip+utf8json",
		"transcript id=raw-asr role=raw-asr default=yes",
		"speech_to_text backend=local-asr engine=asr-engine model=meeting-model",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inspect output lacks %q:\n%s", want, got)
		}
	}
}

func TestInspectPathPublishedMeetingSurfacesOriginAndSummary(t *testing.T) {
	requireFFMediaTools(t)
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "meeting.opus"), portableFixtureOptions{
		words: []string{"Hello", "team"}, withOrigin: true, withSummary: true,
	})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect portable opus: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"origin room_id=rm_9f2a1c3d4e5b6a70 job_id=01K3Q7W8ZC9F0MJXQ2NB8V4RTD attempt=2",
		"meeting_summary backend=openai-compatible",
		"summary format=markdown model=summary-model templateVersion=v0",
		"attachment name=summary.md mime=text/markdown bytes=18",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inspect output lacks %q:\n%s", want, got)
		}
	}
}

func TestInspectPathPublishedMeetingDetectsStaleAudio(t *testing.T) {
	requireFFMediaTools(t)
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "stale.opus"), portableFixtureOptions{
		words: []string{"Hello"}, stale: true,
	})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect stale portable opus: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "cassini=stale-audio") || !strings.Contains(got, "fallback=plain-audio") {
		t.Fatalf("expected stale-audio fallback, got %q", got)
	}
}

func TestDecodePortableMeetingRejectsUnsupportedFormats(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]string)
		want string
	}{
		{
			name: "format tag",
			edit: func(tags map[string]string) { tags["CASSINI_FORMAT"] = "org.example.unsupported/9" },
			want: "unsupported CASSINI_FORMAT",
		},
		{
			name: "manifest version",
			edit: func(tags map[string]string) {
				rewriteMainManifest(t, tags, func(doc map[string]any) { doc["version"] = float64(portable.WireVersion + 1) })
			},
			want: "unsupported payload version",
		},
		{
			name: "missing transcript index",
			edit: func(tags map[string]string) {
				rewriteMainManifest(t, tags, func(doc map[string]any) { delete(doc, "transcripts") })
			},
			want: "transcripts must contain",
		},
		{
			name: "integrity policy",
			edit: func(tags map[string]string) {
				rewriteMainManifest(t, tags, func(doc map[string]any) {
					doc["integrity"].(map[string]any)["matchPolicy"] = "unsupported-policy"
				})
			},
			want: "unsupported audio integrity",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := buildPublishedPortableTags(t, portableFixtureOptions{words: []string{"Hello", "team"}}, nil)
			tc.edit(tags)
			if _, _, err := decodePortableMeeting(tags); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want message containing %q", err, tc.want)
			}
		})
	}
}

func TestPublishedTranscriptChunkSetIsReadAndHolesFailClosed(t *testing.T) {
	words := []string{"Hello", "team", "lantern", "festival", "tonight"}
	tags := buildPublishedPortableTags(t, portableFixtureOptions{words: words}, nil)
	_, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		t.Fatalf("decodePortableMeeting: %v", err)
	}

	bodies := readPortableTranscriptBodies(tags, manifest)
	if got := bodies.WordCounts[portable.RoleRawASR]; got != len(words) {
		t.Fatalf("decoded word count = %d, want %d", got, len(words))
	}
	if len(bodies.Unreadable) != 0 {
		t.Fatalf("whole chunk set reported unreadable: %v", bodies.Unreadable)
	}

	prefix := portable.TranscriptIDToTagPrefix(portable.RoleRawASR)
	count, _ := strconv.Atoi(tags[prefix+"CHUNK_COUNT"])
	delete(tags, fmt.Sprintf("%s%03d", prefix, count-1))
	bodies = readPortableTranscriptBodies(tags, manifest)
	if len(bodies.Unreadable) != 1 || bodies.Unreadable[0] != portable.RoleRawASR {
		t.Fatalf("Unreadable = %v, want [raw-asr]", bodies.Unreadable)
	}
	if err := bodies.err("meeting.opus"); err == nil {
		t.Fatal("missing transcript chunk was accepted")
	}
}

func TestPublishedDerivedTranscriptChunkSetIsReadAndHolesFailClosed(t *testing.T) {
	tags := buildPublishedPortableTags(t, portableFixtureOptions{
		words:       []string{"Hello", "team"},
		withDerived: true,
	}, nil)
	_, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		t.Fatalf("decodePortableMeeting: %v", err)
	}
	if len(manifest.ReadableTranscripts) != 1 {
		t.Fatalf("readable transcripts = %d, want 1", len(manifest.ReadableTranscripts))
	}

	bodies := readPortableTranscriptBodies(tags, manifest)
	if len(bodies.Unreadable) != 0 {
		t.Fatalf("whole derived chunk set reported unreadable: %v", bodies.Unreadable)
	}

	entry := manifest.ReadableTranscripts[0]
	prefix := portable.TranscriptIDToTagPrefix(entry.ID)
	delete(tags, fmt.Sprintf("%s%03d", prefix, entry.PayloadRef.ChunkCount-1))
	bodies = readPortableTranscriptBodies(tags, manifest)
	if len(bodies.Unreadable) != 1 || bodies.Unreadable[0] != entry.ID {
		t.Fatalf("Unreadable = %v, want [%s]", bodies.Unreadable, entry.ID)
	}
}

type portableFixtureOptions struct {
	words       []string
	stale       bool
	withSummary bool
	withOrigin  bool
	withDerived bool
}

func createPortableOpusFixture(t *testing.T, outPath string, opts portableFixtureOptions) string {
	t.Helper()
	basePath := createTestOpus(t, outPath+".audio.opus")
	audio := readTestOpusIntegrity(t, basePath)
	tags := buildPublishedPortableTags(t, opts, &audio)

	args := []string{"-y", "-v", "error", "-i", basePath, "-map", "0:a:0", "-c", "copy"}
	for key, value := range tags {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", key, value))
	}
	args = append(args, outPath)
	if err := runCommand("ffmpeg", args...); err != nil {
		t.Fatalf("write portable Opus tags: %v", err)
	}
	return outPath
}

const portableTestChunkSize = 64

func buildPublishedPortableTags(t *testing.T, opts portableFixtureOptions, audio *portable.OpusAudioIntegrity) map[string]string {
	t.Helper()
	if len(opts.words) == 0 {
		opts.words = []string{"Hello", "team"}
	}
	identity := portable.OpusAudioIntegrity{
		SHA256: strings.Repeat("a", 64), SampleRate: 48000, Channels: 1,
		SampleCount: 9600, DurationMS: 200,
	}
	if audio != nil {
		identity = *audio
	}
	if opts.stale {
		identity.SHA256 = strings.Repeat("0", 64)
	}
	items := make([]portable.TranscriptItem, 0, len(opts.words))
	for i, word := range opts.words {
		items = append(items, portable.TranscriptItem{
			Speaker: "spk1", StartMS: int64(i * 100), EndMS: int64(i*100 + 80), Text: word,
		})
	}
	manifest := portable.NormalizePublishedManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID: "mtg_" + strings.Repeat("c", 64), Title: "Weekly Sync",
			CreatedAtUTC: "2026-03-11T08:30:00Z", DurationMS: identity.DurationMS, Language: "en",
		},
		Audio: portable.Audio{
			Container: "ogg", Codec: "opus", SampleRate: identity.SampleRate, Channels: identity.Channels,
			SampleCount: identity.SampleCount, DurationMS: identity.DurationMS,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: identity.SHA256,
			SampleRate: identity.SampleRate, Channels: identity.Channels,
			SampleCount: identity.SampleCount, DurationMS: identity.DurationMS,
		},
		Speakers: []portable.Speaker{{ID: "spk1", Label: "Silvio"}},
		Provenance: &portable.Provenance{SpeechToText: &portable.ProcessingStep{
			Backend: "local-asr", Engine: "asr-engine", Model: "meeting-model", Device: "cpu", Language: "en",
		}},
	})
	if opts.withOrigin {
		manifest.Meeting.RoomID = "rm_9f2a1c3d4e5b6a70"
		manifest.Meeting.JobID = "01K3Q7W8ZC9F0MJXQ2NB8V4RTD"
		manifest.Meeting.AttemptNumber = 2
	}
	if opts.withSummary {
		manifest.Provenance.MeetingSummary = &portable.ProcessingStep{Backend: "openai-compatible", Model: "summary-model"}
		manifest.Summary = map[string]any{"model": "summary-model", "format": "markdown", "templateVersion": "v0"}
		manifest.Attachments = []map[string]any{{
			"name": "summary.md", "mime": "text/markdown",
			"contentBase64": base64.StdEncoding.EncodeToString([]byte("# Meeting Summary\n")),
		}}
	}
	inputs := []portable.TranscriptInput{{
		ID: portable.RoleRawASR, Role: portable.RoleRawASR, Default: true,
		Language: "en", Provenance: manifest.Provenance.SpeechToText,
		Body: portable.TranscriptBody{
			Format: "cassini.words.v1", Language: "en", WordCount: len(items), Items: items,
		},
	}}
	if opts.withDerived {
		inputs = append(inputs, portable.TranscriptInput{
			ID:                 "readable",
			Role:               portable.RoleReadableCleanup,
			Default:            true,
			Format:             "transcript.readable.v1",
			SourceTranscriptID: portable.RoleRawASR,
			Body: map[string]any{
				"version": "transcript.readable.v1",
				"segments": []any{map[string]any{
					"id": "readable-1", "text": strings.Join(opts.words, " "),
				}},
			},
		})
	}
	encoded, err := portable.EncodePublishedManifest(manifest, inputs, portableTestChunkSize)
	if err != nil {
		t.Fatalf("encode published manifest: %v", err)
	}
	return portable.BuildPublishedOpusTags(manifest, encoded, portable.RoleRawASR)
}

func rewriteMainManifest(t *testing.T, tags map[string]string, mutate func(map[string]any)) {
	t.Helper()
	payload, _, err := decodePortableMeeting(tags)
	if err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload.JSON, &document); err != nil {
		t.Fatalf("parse fixture manifest: %v", err)
	}
	mutate(document)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode modified manifest: %v", err)
	}
	encoded, err := portable.EncodePayloadBytes(raw, portableTestChunkSize)
	if err != nil {
		t.Fatalf("encode modified payload: %v", err)
	}
	for key := range tags {
		if isNumberedPayloadChunk(key) {
			delete(tags, key)
		}
	}
	tags["CASSINI_PAYLOAD_CHUNK_COUNT"] = strconv.Itoa(len(encoded.Chunks))
	tags["CASSINI_PAYLOAD_SHA256"] = encoded.SHA256
	tags["CASSINI_PAYLOAD_RAW_BYTES"] = strconv.Itoa(encoded.RawBytes)
	tags["CASSINI_PAYLOAD_GZIP_BYTES"] = strconv.Itoa(encoded.CompressedBytes)
	for i, chunk := range encoded.Chunks {
		tags[fmt.Sprintf("CASSINI_PAYLOAD_%03d", i)] = chunk
	}
}

func isNumberedPayloadChunk(key string) bool {
	const prefix = "CASSINI_PAYLOAD_"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(key, prefix)
	if suffix == "" {
		return false
	}
	_, err := strconv.Atoi(suffix)
	return err == nil
}

func requireFFMediaTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
}

func createTestOpus(t *testing.T, outPath string) string {
	t.Helper()
	if err := runCommand("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i",
		"sine=frequency=880:sample_rate=48000:duration=0.2", "-c:a", "libopus", "-application", "voip", outPath); err != nil {
		t.Fatalf("create test Opus: %v", err)
	}
	return outPath
}

func readTestOpusIntegrity(t *testing.T, path string) portable.OpusAudioIntegrity {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open Opus fixture: %v", err)
	}
	defer file.Close()
	integrity, err := portable.ComputeOpusAudioIntegrity(file)
	if err != nil {
		t.Fatalf("compute Opus fixture integrity: %v", err)
	}
	return integrity
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(string(output)))
	}
	return nil
}
