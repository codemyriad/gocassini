package inspect

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
}

func createPortableOpusFixture(t *testing.T, outPath string, opts portableFixtureOptions) string {
	t.Helper()

	basePath := createTestOpus(t, filepath.Join(filepath.Dir(outPath), "base.opus"))
	meta, err := probePortableAudio(basePath)
	if err != nil {
		t.Fatalf("probe base opus: %v", err)
	}
	stream, err := firstAudioStream(meta)
	if err != nil {
		t.Fatalf("first audio stream: %v", err)
	}
	sampleRate := parseIntOrZero(stream.SampleRate)
	if sampleRate == 0 {
		t.Fatalf("expected sample rate")
	}
	channels := stream.Channels
	if channels == 0 {
		t.Fatalf("expected channels")
	}
	pcmBytes, err := decodeAudioPCM(basePath, sampleRate, channels)
	if err != nil {
		t.Fatalf("decode pcm: %v", err)
	}
	pcmSum := sha256.Sum256(pcmBytes)
	pcmSHA := hex.EncodeToString(pcmSum[:])
	sampleCount := int64(len(pcmBytes) / (2 * channels))
	durationMS := int64(sampleCount * 1000 / int64(sampleRate))
	if opts.stale {
		pcmSHA = strings.Repeat("0", 64)
	}

	manifest := portable.NormalizeManifest(portable.Manifest{
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
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy,
			PCMFormat:   portable.AudioPCMFormat,
			PCMSHA256:   pcmSHA,
			SampleRate:  sampleRate,
			Channels:    channels,
			SampleCount: sampleCount,
			DurationMS:  durationMS,
		},
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
	})
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
	payload, err := portable.EncodeManifest(manifest, 256)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
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
	for key, value := range portable.BuildOpusTags(manifest, payload) {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", key, value))
	}
	args = append(args, outPath)

	if err := runCommand("ffmpeg", args...); err != nil {
		t.Fatalf("write portable opus tags: %v", err)
	}
	return outPath
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(string(output)))
	}
	return nil
}
