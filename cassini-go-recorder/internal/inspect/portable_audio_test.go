package inspect

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

func TestInspectPathPlainOpusFallsBackToPlainAudio(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	path := createTestOpus(t, filepath.Join(tmp, "plain.opus"))

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect plain opus: %v", err)
	}
	if !strings.Contains(out.String(), "cassini=plain-audio") {
		t.Fatalf("expected plain-audio fallback, got %q", out.String())
	}
}

func TestInspectPathPortableMeetingOpus(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "meeting.opus"), portableFixtureOptions{})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect portable opus: %v", err)
	}
	if !strings.Contains(out.String(), "portable_meeting="+path) {
		t.Fatalf("expected portable meeting summary, got %q", out.String())
	}
	if !strings.Contains(out.String(), "cassini=ok") {
		t.Fatalf("expected integrity ok, got %q", out.String())
	}
	if !strings.Contains(out.String(), "payload encoding=base64url+gzip+utf8json") {
		t.Fatalf("expected gzip payload encoding, got %q", out.String())
	}
	if !strings.Contains(out.String(), "speech_to_text backend=local-whisper engine=faster-whisper model=large-v3") {
		t.Fatalf("expected speech-to-text provenance, got %q", out.String())
	}
	if strings.Contains(out.String(), "meeting_summary ") {
		t.Errorf("did not expect meeting_summary line when summary absent, got %q", out.String())
	}
	if strings.Contains(out.String(), "summary ") {
		t.Errorf("did not expect summary metadata line when summary absent, got %q", out.String())
	}
	if strings.Contains(out.String(), "attachment ") {
		t.Errorf("did not expect attachment line when summary absent, got %q", out.String())
	}
	// A file packed by hand genuinely has no origin, and a row of dashes would
	// read like a lookup that failed rather than like nothing to report.
	if strings.Contains(out.String(), "origin ") {
		t.Errorf("did not expect an origin line when the meeting has no room or job, got %q", out.String())
	}
}

func TestInspectPathPortableV3UsesCompressedOpusIntegrity(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "meeting-v3.opus"), portableFixtureOptions{version3: true})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect portable v3 opus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "cassini=ok") || !strings.Contains(got, "opus_sha256=") {
		t.Fatalf("expected compressed Opus integrity success, got %q", got)
	}
	if strings.Contains(got, "pcm_sha256=") {
		t.Fatalf("v3 unexpectedly decoded PCM: %q", got)
	}
}

func TestLegacyPCMPolicyWithoutDigestFailsClosed(t *testing.T) {
	requireFFMediaTools(t)

	path := createTestOpus(t, filepath.Join(t.TempDir(), "missing-pcm-digest.opus"))
	meta, err := probePortableAudio(path)
	if err != nil {
		t.Fatalf("probe fixture: %v", err)
	}
	if len(meta.Streams) == 0 {
		t.Fatal("fixture has no audio stream")
	}
	_, err = verifyPortableLegacyPCMIntegrity(path, meta.Streams[0], nil, portable.Manifest{
		Integrity: portable.Integrity{MatchPolicy: portable.LegacyAudioMatchPolicyPCM},
	})
	if err == nil || !strings.Contains(err.Error(), "has no pcmSha256") {
		t.Fatalf("missing legacy digest error = %v", err)
	}
}

// `cassini inspect` is what the docs point people at to check by hand that a
// recording carries its room. Until D-640 it decoded the room and dropped it, so
// the only way to verify the producer chain was a raw ffprobe against tag names
// the docs did not list — a poor answer then, and a worse one now that a
// maintenance tool can rewrite these fields and an operator has to confirm the
// rewrite landed.
func TestInspectPathPortableMeetingOpusSurfacesTheOrigin(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "with-origin.opus"), portableFixtureOptions{
		withOrigin: true,
	})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect portable opus: %v", err)
	}
	if !strings.Contains(out.String(), "origin room_id=rm_9f2a1c3d4e5b6a70 room_name=- job_id=01K3Q7W8ZC9F0MJXQ2NB8V4RTD attempt=2\n") {
		t.Errorf("expected an origin line naming the room and the job, got %q", out.String())
	}
}

func TestInspectPathPortableMeetingOpusSurfacesSummary(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "with-summary.opus"), portableFixtureOptions{
		withSummary: true,
	})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect portable opus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "meeting_summary backend=openai-compatible") {
		t.Errorf("expected meeting_summary provenance line, got %q", got)
	}
	if !strings.Contains(got, "model=summary-model") {
		t.Errorf("expected summary model in meeting_summary line, got %q", got)
	}
	if !strings.Contains(got, "summary format=markdown model=summary-model templateVersion=v0\n") {
		t.Errorf("expected summary metadata line with sorted keys, got %q", got)
	}
	if !strings.Contains(got, "attachment name=summary.md mime=text/markdown bytes=") {
		t.Errorf("expected attachment line for summary.md, got %q", got)
	}
}

func TestInspectPathPortableMeetingOpusDetectsStaleAudio(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	path := createPortableOpusFixture(t, filepath.Join(tmp, "stale.opus"), portableFixtureOptions{stale: true})

	var out bytes.Buffer
	if err := InspectPath(&out, path); err != nil {
		t.Fatalf("inspect stale portable opus: %v", err)
	}
	if !strings.Contains(out.String(), "cassini=stale-audio") {
		t.Fatalf("expected stale-audio status, got %q", out.String())
	}
	if !strings.Contains(out.String(), "fallback=plain-audio") {
		t.Fatalf("expected plain-audio fallback, got %q", out.String())
	}
}

func TestExtractTranscriptWordsFromV2Opus(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	wantWords := []string{"Hello", "team", "lantern", "festival", "tonight"}
	path := createPortableV2OpusFixture(t, filepath.Join(tmp, "v2.opus"), wantWords)

	extracted, err := ExtractTranscriptWords(path)
	if err != nil {
		t.Fatalf("ExtractTranscriptWords: %v", err)
	}
	if extracted.TranscriptID != portable.RoleRawASR {
		t.Errorf("expected default transcript id %q, got %q", portable.RoleRawASR, extracted.TranscriptID)
	}
	got := make([]string, 0, len(extracted.Words))
	for _, w := range extracted.Words {
		got = append(got, w.Text)
	}
	if strings.Join(got, " ") != strings.Join(wantWords, " ") {
		t.Fatalf("recovered words %v, want %v", got, wantWords)
	}

	var rendered bytes.Buffer
	if err := WriteTranscriptWordsV1JSON(&rendered, extracted); err != nil {
		t.Fatalf("WriteTranscriptWordsV1JSON: %v", err)
	}
	if !strings.Contains(rendered.String(), `"version": "transcript.words.v1"`) {
		t.Fatalf("expected transcript.words.v1 doc, got %q", rendered.String())
	}
	for _, w := range wantWords {
		if !strings.Contains(rendered.String(), `"text": "`+w+`"`) {
			t.Fatalf("rendered transcript missing word %q: %s", w, rendered.String())
		}
	}
}

// portableV2FixtureOptions varies a v2 portable fixture beyond the plain
// single-speaker word list: an explicit per-word speaker assignment (so speaker
// changes can be exercised) and an attached summary.md.
type portableV2FixtureOptions struct {
	words []string
	// speakerOf assigns a speaker id per word index. Nil means every word is
	// spoken by spk1.
	speakerOf func(index int) string
	// speakers declares the speaker roster. Nil means the spk1/Silvio default.
	speakers    []portable.Speaker
	withSummary bool
	summaryBody string
	// dropLastTranscriptChunk leaves a hole in the raw-ASR chunk set, the way a
	// metadata editor that dropped one comment does: every count still names
	// the chunk that is gone.
	dropLastTranscriptChunk bool
}

func createPortableV2OpusFixture(t *testing.T, outPath string, words []string) string {
	t.Helper()
	return createPortableV2OpusFixtureWith(t, outPath, portableV2FixtureOptions{words: words})
}

func createPortableV2OpusFixtureWith(t *testing.T, outPath string, opts portableV2FixtureOptions) string {
	t.Helper()
	words := opts.words
	speakerOf := opts.speakerOf
	if speakerOf == nil {
		speakerOf = func(int) string { return "spk1" }
	}
	speakers := opts.speakers
	if speakers == nil {
		speakers = []portable.Speaker{{ID: "spk1", Label: "Silvio"}}
	}

	basePath := createTestOpus(t, filepath.Join(filepath.Dir(outPath), "base-v2.opus"))
	audioIdentity := readTestOpusIntegrity(t, basePath)
	sampleRate := audioIdentity.SampleRate
	channels := audioIdentity.Channels
	pcmSHA, pcmByteCount, err := hashDecodedAudioPCM(basePath, sampleRate, channels)
	if err != nil {
		t.Fatalf("hash decoded PCM: %v", err)
	}
	sampleCount := pcmByteCount / int64(2*channels)
	durationMS := sampleCount * 1000 / int64(sampleRate)

	items := make([]portable.TranscriptItem, 0, len(words))
	for i, w := range words {
		items = append(items, portable.TranscriptItem{
			Speaker: speakerOf(i),
			StartMS: int64(i * 100),
			EndMS:   int64(i*100 + 80),
			Text:    w,
		})
	}
	manifest := portable.NormalizeManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID:           "meeting-v2",
			Title:        "Lantern Festival",
			CreatedAtUTC: "2026-03-11T08:30:00Z",
			DurationMS:   durationMS,
			Language:     "en",
		},
		Audio: portable.Audio{
			Container:   "ogg",
			Codec:       "opus",
			SampleRate:  sampleRate,
			Channels:    channels,
			SampleCount: sampleCount,
			DurationMS:  durationMS,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.LegacyAudioMatchPolicyPCM,
			PCMFormat:   portable.AudioPCMFormat,
			PCMSHA256:   pcmSHA,
			SampleRate:  sampleRate,
			Channels:    channels,
			SampleCount: sampleCount,
			DurationMS:  durationMS,
		},
		Speakers: speakers,
	})
	if opts.withSummary {
		body := opts.summaryBody
		if body == "" {
			body = "# Meeting Summary\n"
		}
		manifest.Summary = map[string]any{
			"model":           "summary-model",
			"format":          "markdown",
			"templateVersion": "v0",
		}
		manifest.Attachments = append(manifest.Attachments, map[string]any{
			"name":          "summary.md",
			"mime":          "text/markdown",
			"contentBase64": base64.StdEncoding.EncodeToString([]byte(body)),
		})
	}

	input := portable.TranscriptInput{
		ID:       portable.RoleRawASR,
		Role:     portable.RoleRawASR,
		Default:  true,
		Language: "en",
		Body: portable.TranscriptBody{
			Format:    "cassini.words.v1",
			Language:  "en",
			WordCount: len(items),
			Items:     items,
		},
	}
	encoded, err := portable.EncodeManifestV2(manifest, []portable.TranscriptInput{input}, 256)
	if err != nil {
		t.Fatalf("encode v2 manifest: %v", err)
	}
	tags := portable.BuildOpusTagsV2(manifest, encoded, portable.RoleRawASR)
	if opts.dropLastTranscriptChunk {
		prefix := portable.TranscriptIDToTagPrefix(portable.RoleRawASR)
		delete(tags, fmt.Sprintf("%s%03d", prefix, parseIntOrZero(tags[prefix+"CHUNK_COUNT"])-1))
	}

	args := []string{"-y", "-v", "error", "-i", basePath, "-map", "0:a:0", "-c", "copy"}
	for key, value := range tags {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", key, value))
	}
	args = append(args, outPath)
	if err := runCommand("ffmpeg", args...); err != nil {
		t.Fatalf("write v2 portable opus tags: %v", err)
	}
	return outPath
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
	if err := runCommand("ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=880:sample_rate=48000:duration=0.2",
		"-c:a", "libopus",
		"-application", "voip",
		outPath,
	); err != nil {
		t.Fatalf("create test opus: %v", err)
	}
	return outPath
}

type portableFixtureOptions struct {
	stale       bool
	withSummary bool
	withOrigin  bool
	version3    bool
}

func createPortableOpusFixture(t *testing.T, outPath string, opts portableFixtureOptions) string {
	t.Helper()

	basePath := createTestOpus(t, filepath.Join(filepath.Dir(outPath), "base.opus"))
	audioIdentity := readTestOpusIntegrity(t, basePath)
	sampleRate := audioIdentity.SampleRate
	channels := audioIdentity.Channels
	sampleCount := audioIdentity.SampleCount
	durationMS := audioIdentity.DurationMS
	integrity := portable.Integrity{
		MatchPolicy: portable.AudioMatchPolicy,
		OpusSHA256:  audioIdentity.SHA256,
		SampleRate:  sampleRate,
		Channels:    channels,
		SampleCount: sampleCount,
		DurationMS:  durationMS,
	}
	if !opts.version3 {
		pcmSHA, pcmByteCount, err := hashDecodedAudioPCM(basePath, sampleRate, channels)
		if err != nil {
			t.Fatalf("hash decoded PCM: %v", err)
		}
		sampleCount = pcmByteCount / int64(2*channels)
		durationMS = sampleCount * 1000 / int64(sampleRate)
		integrity = portable.Integrity{
			MatchPolicy: portable.LegacyAudioMatchPolicyPCM,
			PCMFormat:   portable.AudioPCMFormat,
			PCMSHA256:   pcmSHA,
			SampleRate:  sampleRate,
			Channels:    channels,
			SampleCount: sampleCount,
			DurationMS:  durationMS,
		}
	}
	if opts.stale {
		if opts.version3 {
			integrity.OpusSHA256 = strings.Repeat("0", 64)
		} else {
			integrity.PCMSHA256 = strings.Repeat("0", 64)
		}
	}

	manifest := portable.Manifest{
		Meeting: portable.Meeting{
			ID:           "meeting-20260311-weekly-sync",
			Title:        "Weekly Sync",
			CreatedAtUTC: "2026-03-11T08:30:00Z",
			DurationMS:   durationMS,
			Language:     "en",
		},
		Audio: portable.Audio{
			Container:   "ogg",
			Codec:       "opus",
			SampleRate:  sampleRate,
			Channels:    channels,
			SampleCount: sampleCount,
			DurationMS:  durationMS,
		},
		Integrity: integrity,
		Speakers: []portable.Speaker{
			{ID: "spk1", Label: "Silvio"},
		},
		Transcript: portable.Transcript{
			Format:    "cassini.words.v1",
			Language:  "en",
			WordCount: 2,
			Items: []portable.TranscriptItem{
				{Speaker: "spk1", StartMS: 0, EndMS: 120, Text: "Hello"},
				{Speaker: "spk1", StartMS: 140, EndMS: 260, Text: "team"},
			},
		},
		Provenance: &portable.Provenance{
			SpeechToText: &portable.ProcessingStep{
				Backend:  "local-whisper",
				Engine:   "faster-whisper",
				Model:    "large-v3",
				Device:   "cuda",
				Language: "en",
			},
			ReadableCleanup: &portable.ProcessingStep{
				Backend: "local-llama-cli",
				Engine:  "llama.cpp",
				Model:   "model-Q4_K_M.gguf",
				Source:  "generated",
			},
		},
	}
	if opts.version3 {
		manifest = portable.NormalizeManifestV3(manifest)
	} else {
		manifest = portable.NormalizeManifest(manifest)
	}
	if opts.withOrigin {
		manifest.Meeting.RoomID = "rm_9f2a1c3d4e5b6a70"
		manifest.Meeting.JobID = "01K3Q7W8ZC9F0MJXQ2NB8V4RTD"
		manifest.Meeting.AttemptNumber = 2
	}
	if opts.withSummary {
		manifest.Provenance.MeetingSummary = &portable.ProcessingStep{
			Backend: "openai-compatible",
			Model:   "summary-model",
		}
		manifest.Summary = map[string]any{
			"model":           "summary-model",
			"format":          "markdown",
			"templateVersion": "v0",
		}
		manifest.Attachments = append(manifest.Attachments, map[string]any{
			"name":          "summary.md",
			"mime":          "text/markdown",
			"contentBase64": base64.StdEncoding.EncodeToString([]byte("# Meeting Summary\n")),
		})
	}
	args := []string{
		"-y",
		"-v", "error",
		"-i", basePath,
		"-map", "0:a:0",
		"-c", "copy",
		"-metadata", "TITLE=Weekly Sync",
		"-metadata", "DESCRIPTION=Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON.",
	}
	tags := map[string]string{}
	if opts.version3 {
		input := portable.TranscriptInput{
			ID:         portable.RoleRawASR,
			Role:       portable.RoleRawASR,
			Default:    true,
			Language:   manifest.Transcript.Language,
			Provenance: manifest.Provenance.SpeechToText,
			Body: portable.TranscriptBody{
				Format:    manifest.Transcript.Format,
				Language:  manifest.Transcript.Language,
				WordCount: manifest.Transcript.WordCount,
				Items:     manifest.Transcript.Items,
			},
		}
		encoded, err := portable.EncodeManifestV3(manifest, []portable.TranscriptInput{input}, 256)
		if err != nil {
			t.Fatalf("encode v3 manifest: %v", err)
		}
		tags = portable.BuildOpusTagsV3(manifest, encoded, portable.RoleRawASR)
	} else {
		payload, err := portable.EncodeManifest(manifest, 256)
		if err != nil {
			t.Fatalf("encode v1 manifest: %v", err)
		}
		tags = portable.BuildOpusTags(manifest, payload)
	}
	for key, value := range tags {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", key, value))
	}
	args = append(args, outPath)

	if err := runCommand("ffmpeg", args...); err != nil {
		t.Fatalf("write portable opus tags: %v", err)
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

// A padded chunk set is a cosmetic difference, not a damaged file. Producers
// write base64url unpadded, but SPEC.md's worked example pipes the chunks
// through a base64 tool that pads, and most languages' encoders pad by default,
// so a reader that refuses '=' loses a transcript it could have read. The
// wrapped case is here because a Vorbis comment value is arbitrary UTF-8: a
// tool that rewrapped a long chunk may legally have left a newline in it, and
// the padding has to be judged after the whitespace goes, not before.
func TestDecodePortablePayloadAcceptsPaddedBase64URL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rewrite func(encoded string) string
	}{
		{name: "unpadded", rewrite: func(encoded string) string { return encoded }},
		{name: "padded", rewrite: padBase64URL},
		{name: "padded and wrapped", rewrite: func(encoded string) string {
			return "\n" + padBase64URL(encoded) + "\n"
		}},
		{name: "wrapped", rewrite: func(encoded string) string { return encoded + "\r\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := []string{"Hello", "team", "lantern", "festival", "tonight"}
			tags := buildPortableV3Tags(t, words)
			rewriteChunkSet(t, tags, "CASSINI_PAYLOAD_", tc.rewrite)
			rewriteChunkSet(t, tags, portable.TranscriptIDToTagPrefix(portable.RoleRawASR), tc.rewrite)

			payload, manifest, err := decodePortableMeeting(tags)
			if err != nil {
				t.Fatalf("decodePortableMeeting: %v", err)
			}
			if manifest.Version != 3 {
				t.Fatalf("Manifest.Version = %d, want 3", manifest.Version)
			}
			if payload.RawBytes != len(payload.JSON) {
				t.Errorf("RawBytes = %d, want %d", payload.RawBytes, len(payload.JSON))
			}
			if len(manifest.Transcripts) != 1 {
				t.Fatalf("decoded %d transcripts, want 1", len(manifest.Transcripts))
			}
			body, _, err := decodeTranscriptBody(tags, manifest.Transcripts[0])
			if err != nil {
				t.Fatalf("decodeTranscriptBody: %v", err)
			}
			if body.WordCount != len(words) {
				t.Errorf("body.WordCount = %d, want %d", body.WordCount, len(words))
			}
		})
	}
}

// padBase64URL appends the '=' padding an unpadded base64url string would carry
// had it been produced by a padding encoder.
func padBase64URL(encoded string) string {
	if remainder := len(encoded) % 4; remainder != 0 {
		return encoded + strings.Repeat("=", 4-remainder)
	}
	return encoded
}

// rewriteChunkSet reassembles one chunk set, hands the concatenated text to
// rewrite, and writes the result back over the same numbered tags — adjusting
// the chunk count when the rewrite changes how many chunks it takes.
func rewriteChunkSet(t *testing.T, tags map[string]string, prefix string, rewrite func(string) string) {
	t.Helper()
	count := parseIntOrZero(tags[prefix+"CHUNK_COUNT"])
	if count <= 0 {
		t.Fatalf("chunk set %s has no chunks", prefix)
	}
	var encoded strings.Builder
	for idx := 0; idx < count; idx++ {
		key := fmt.Sprintf("%s%03d", prefix, idx)
		encoded.WriteString(tags[key])
		delete(tags, key)
	}
	chunks := portable.ChunkString(rewrite(encoded.String()), portableTestChunkSize)
	for idx, chunk := range chunks {
		tags[fmt.Sprintf("%s%03d", prefix, idx)] = chunk
	}
	tags[prefix+"CHUNK_COUNT"] = fmt.Sprintf("%d", len(chunks))
}

// portableTestChunkSize is small enough that every fixture below spans several
// chunks, which is the only way a reassembly bug shows up at all.
const portableTestChunkSize = 64

// buildPortableV3Tags builds the OpusTag set of a one-transcript v3 file
// without going near ffmpeg. The reader's tag layer takes a map, so a decode
// test does not need a container, an audio stream, or the tools to make one.
func buildPortableV3Tags(t *testing.T, words []string) map[string]string {
	t.Helper()
	items := make([]portable.TranscriptItem, 0, len(words))
	for i, word := range words {
		items = append(items, portable.TranscriptItem{
			Speaker: "spk1",
			StartMS: int64(i * 100),
			EndMS:   int64(i*100 + 80),
			Text:    word,
		})
	}
	manifest := portable.NormalizeManifestV3(portable.Manifest{
		Meeting: portable.Meeting{
			ID:           "meeting-v3",
			Title:        "Lantern Festival",
			CreatedAtUTC: "2026-03-11T08:30:00Z",
			DurationMS:   int64(len(words) * 100),
			Language:     "en",
		},
		Audio: portable.Audio{Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy,
			OpusSHA256:  strings.Repeat("a", 64),
		},
		Speakers: []portable.Speaker{{ID: "spk1", Label: "Silvio"}},
	})
	input := portable.TranscriptInput{
		ID:       portable.RoleRawASR,
		Role:     portable.RoleRawASR,
		Default:  true,
		Language: "en",
		Body: portable.TranscriptBody{
			Format:    "cassini.words.v1",
			Language:  "en",
			WordCount: len(items),
			Items:     items,
		},
	}
	encoded, err := portable.EncodeManifestV3(manifest, []portable.TranscriptInput{input}, portableTestChunkSize)
	if err != nil {
		t.Fatalf("encode v3 manifest: %v", err)
	}
	return portable.BuildOpusTagsV3(manifest, encoded, portable.RoleRawASR)
}

// buildPortableV3TagsTwoTranscripts builds the tag set of a v3 file carrying
// two raw transcripts, with defaultRawID written into CASSINI_TRANSCRIPT_DEFAULT
// so a caller can put that tag and the manifest's `default` flag at odds.
func buildPortableV3TagsTwoTranscripts(t *testing.T, defaultRawID string) map[string]string {
	t.Helper()
	tags := buildPortableV3Tags(t, []string{"Hello", "team", "again"})
	manifest := portable.NormalizeManifestV3(portable.Manifest{
		Meeting:   portable.Meeting{ID: "meeting-v3", Title: "Lantern Festival", CreatedAtUTC: "2026-03-11T08:30:00Z", Language: "en"},
		Audio:     portable.Audio{Container: "ogg", Codec: "opus", SampleRate: 48000, Channels: 1},
		Integrity: portable.Integrity{MatchPolicy: portable.AudioMatchPolicy, OpusSHA256: strings.Repeat("a", 64)},
		Speakers:  []portable.Speaker{{ID: "spk1", Label: "Silvio"}},
	})
	inputs := []portable.TranscriptInput{
		{
			ID: portable.RoleRawASR, Role: portable.RoleRawASR, Default: true, Language: "en",
			Body: portable.TranscriptBody{Format: "cassini.words.v1", Language: "en", WordCount: 3, Items: []portable.TranscriptItem{
				{Speaker: "spk1", StartMS: 0, EndMS: 80, Text: "Hello"},
				{Speaker: "spk1", StartMS: 100, EndMS: 180, Text: "team"},
				{Speaker: "spk1", StartMS: 200, EndMS: 280, Text: "again"},
			}},
		},
		{
			ID: "second-pass", Role: portable.RoleRawASR, Language: "en",
			Body: portable.TranscriptBody{Format: "cassini.words.v1", Language: "en", WordCount: 2, Items: []portable.TranscriptItem{
				{Speaker: "spk1", StartMS: 0, EndMS: 80, Text: "Hello"},
				{Speaker: "spk1", StartMS: 100, EndMS: 180, Text: "team"},
			}},
		},
	}
	encoded, err := portable.EncodeManifestV3(manifest, inputs, portableTestChunkSize)
	if err != nil {
		t.Fatalf("encode two-transcript v3 manifest: %v", err)
	}
	for key := range tags {
		delete(tags, key)
	}
	for key, value := range portable.BuildOpusTagsV3(manifest, encoded, defaultRawID) {
		tags[key] = value
	}
	return tags
}

// The manifest is the record and the CASSINI_TX_<ID>_PAYLOAD_CHUNK_COUNT tag is
// a copy of it, so a reader that has decoded the manifest gathers the number of
// chunks payloadRef names. Believing the tag instead refused a transcript that
// was all there when the tag ran high, and quietly returned a truncated one when
// it ran low — a tool that rewrote one layer and not the other could do either.
func TestDecodeTranscriptBodyPrefersTheManifestChunkCount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		taggedCount string
		wantWarning bool
	}{
		{name: "agrees"},
		{name: "tag too high", taggedCount: "9", wantWarning: true},
		{name: "tag too low", taggedCount: "1", wantWarning: true},
		{name: "tag absent", taggedCount: "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := []string{"Hello", "team", "lantern", "festival", "tonight"}
			tags := buildPortableV3Tags(t, words)
			prefix := portable.TranscriptIDToTagPrefix(portable.RoleRawASR)
			switch tc.taggedCount {
			case "":
			case "-":
				delete(tags, prefix+"CHUNK_COUNT")
			default:
				tags[prefix+"CHUNK_COUNT"] = tc.taggedCount
			}

			_, manifest, err := decodePortableMeeting(tags)
			if err != nil {
				t.Fatalf("decodePortableMeeting: %v", err)
			}
			body, warnings, err := decodeTranscriptBody(tags, manifest.Transcripts[0])
			if err != nil {
				t.Fatalf("decodeTranscriptBody: %v", err)
			}
			if body.WordCount != len(words) || len(body.Items) != len(words) {
				t.Errorf("decoded %d of %d words (wordCount=%d)", len(body.Items), len(words), body.WordCount)
			}
			gotWarning := strings.Join(warnings, "\n")
			if tc.wantWarning && !strings.Contains(gotWarning, "chunk count disagrees between manifest and tag") {
				t.Errorf("warnings = %q, want the tag/manifest disagreement reported", gotWarning)
			}
			if !tc.wantWarning && gotWarning != "" {
				t.Errorf("warnings = %q, want none", gotWarning)
			}
		})
	}
}

// CASSINI_TRANSCRIPT_DEFAULT is the same kind of copy: it exists so a reader
// holding only ffprobe can name the default before it decodes anything, and it
// stops deciding the moment the manifest is in hand. Obeying it let a tool that
// rewrote the tag alone move which transcript every consumer read.
func TestDefaultWordsTranscriptEntryPrefersTheManifestFlag(t *testing.T) {
	for _, tc := range []struct {
		name        string
		taggedID    string
		wantID      string
		wantWarning bool
	}{
		{name: "agrees", taggedID: portable.RoleRawASR, wantID: portable.RoleRawASR},
		{name: "tag names another transcript", taggedID: "second-pass", wantID: portable.RoleRawASR, wantWarning: true},
		{name: "tag absent", taggedID: "", wantID: portable.RoleRawASR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := buildPortableV3TagsTwoTranscripts(t, tc.taggedID)
			_, manifest, err := decodePortableMeeting(tags)
			if err != nil {
				t.Fatalf("decodePortableMeeting: %v", err)
			}
			entry, warnings, ok := defaultWordsTranscriptEntry(tags, manifest)
			if !ok {
				t.Fatal("defaultWordsTranscriptEntry found no default transcript")
			}
			if entry.ID != tc.wantID {
				t.Errorf("default transcript = %q, want %q", entry.ID, tc.wantID)
			}
			gotWarning := strings.Join(warnings, "\n")
			if tc.wantWarning && !strings.Contains(gotWarning, "default transcript disagrees between manifest and tag") {
				t.Errorf("warnings = %q, want the tag/manifest disagreement reported", gotWarning)
			}
			if !tc.wantWarning && gotWarning != "" {
				t.Errorf("warnings = %q, want none", gotWarning)
			}
		})
	}
}

// `words=` is the only number inspect prints about the transcript, and until
// now it was the one number it had not checked: it came from the manifest
// index's declared wordCount, so a file whose chunk set had a hole in it still
// reported `cassini=ok words=900` while the transcript itself was unreachable.
// Only `--transcript` found that out, and even that exited 0.
func TestInspectPathPortableMeetingReportsTheWordsItDecoded(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	words := []string{"Hello", "team", "lantern", "festival", "tonight"}

	t.Run("readable body", func(t *testing.T) {
		path := createPortableV2OpusFixtureWith(t, filepath.Join(tmp, "readable.opus"), portableV2FixtureOptions{
			words: words,
		})
		var out bytes.Buffer
		if err := InspectPath(&out, path); err != nil {
			t.Fatalf("inspect portable opus: %v", err)
		}
		if got := out.String(); !strings.Contains(got, fmt.Sprintf(" words=%d ", len(words))) {
			t.Errorf("expected words=%d, got %q", len(words), got)
		}
	})

	t.Run("unreadable body", func(t *testing.T) {
		path := createPortableV2OpusFixtureWith(t, filepath.Join(tmp, "holed.opus"), portableV2FixtureOptions{
			words:                   words,
			dropLastTranscriptChunk: true,
		})
		var out bytes.Buffer
		err := InspectPath(&out, path)
		if err == nil {
			t.Fatalf("inspect reported success on a file whose transcript body is unreachable: %q", out.String())
		}
		if !strings.Contains(err.Error(), "could not read the transcript body of raw-asr") {
			t.Errorf("error = %v, want it to name the transcript it could not read", err)
		}
		got := out.String()
		if !strings.Contains(got, "cassini=invalid-cassini-metadata") {
			t.Errorf("expected cassini=invalid-cassini-metadata, got %q", got)
		}
		if !strings.Contains(got, " words=0 ") {
			t.Errorf("expected words=0 for a transcript nothing could be read out of, got %q", got)
		}
		if !strings.Contains(got, "warning=transcript raw-asr body could not be read: missing transcript chunk CASSINI_TX_RAW_ASR_PAYLOAD_") {
			t.Errorf("expected a warning naming the missing chunk, got %q", got)
		}
	})
}

// readPortableTranscriptBodies is where that number now comes from: a
// transcript whose body could not be read is absent from the count, not zero
// in it, and it is named so the caller can say which one went missing.
func TestReadPortableTranscriptBodiesSeparatesReadFromDeclared(t *testing.T) {
	words := []string{"Hello", "team", "lantern", "festival", "tonight"}
	tags := buildPortableV3Tags(t, words)
	_, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		t.Fatalf("decodePortableMeeting: %v", err)
	}

	bodies := readPortableTranscriptBodies(tags, manifest)
	if got := bodies.WordCounts[portable.RoleRawASR]; got != len(words) {
		t.Errorf("decoded word count = %d, want %d", got, len(words))
	}
	if len(bodies.Unreadable) != 0 || bodies.err("meeting.opus") != nil {
		t.Errorf("a whole file reported %v unreadable", bodies.Unreadable)
	}

	prefix := portable.TranscriptIDToTagPrefix(portable.RoleRawASR)
	delete(tags, prefix+"001")
	bodies = readPortableTranscriptBodies(tags, manifest)
	if len(bodies.WordCounts) != 0 {
		t.Errorf("word counts = %v, want none from a chunk set with a hole in it", bodies.WordCounts)
	}
	if len(bodies.Unreadable) != 1 || bodies.Unreadable[0] != portable.RoleRawASR {
		t.Errorf("Unreadable = %v, want [raw-asr]", bodies.Unreadable)
	}
	if err := bodies.err("meeting.opus"); err == nil {
		t.Error("err() = nil, want the unreadable transcript reported to the caller")
	}
}
