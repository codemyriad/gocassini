package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFlagsRequiresNamedInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "mkv", args: nil, want: "missing --mkv"},
		{name: "transcript", args: []string{"--mkv", "meeting.mkv"}, want: "missing --transcript"},
		{name: "output", args: []string{"--mkv", "meeting.mkv", "--transcript", "words.json"}, want: "missing --output"},
		{name: "positional", args: []string{"--mkv", "meeting.mkv", "--transcript", "words.json", "--output", "challenges.json", "extra"}, want: "unexpected positional argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFlags(tt.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseFlags() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParseFlagsAcceptsCompleteConfiguration(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--mkv", "meeting.mkv",
		"--transcript", "transcript.words.v1.json",
		"--output", "challenges.v1.json",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.MKVPath != "meeting.mkv" || cfg.TranscriptPath != "transcript.words.v1.json" || cfg.OutputPath != "challenges.v1.json" {
		t.Fatalf("parseFlags() = %#v", cfg)
	}
}

func TestParseFlagsHelpExplainsNoSpeechRecognition(t *testing.T) {
	var output bytes.Buffer
	_, err := parseFlags([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(--help) error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(output.String(), "Waveform analysis only; no speech recognition") {
		t.Fatalf("help does not make execution scope explicit:\n%s", output.String())
	}
}

func TestSamePathRecognizesEquivalentAndHardLinkedPaths(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	linked := filepath.Join(tmp, "linked")
	if err := os.WriteFile(source, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, linked); err != nil {
		t.Fatal(err)
	}
	if !samePath(source, filepath.Join(tmp, ".", "source")) {
		t.Fatal("samePath did not recognize lexically equivalent paths")
	}
	if !samePath(source, linked) {
		t.Fatal("samePath did not recognize hard-linked paths")
	}
	if samePath(source, filepath.Join(tmp, "missing")) {
		t.Fatal("samePath considered distinct paths equal")
	}
}

func TestDecodeTranscriptSegmentsReadsOnlyMinerFields(t *testing.T) {
	input := `{
  "version": "transcript.words.v1",
  "media": {"src": "meeting.webm", "durationMs": 9000, "largeFutureMetadata": [1, 2, 3]},
  "speakers": [{"id": "spk_alice", "label": "Alice"}],
  "futureTopLevelField": {"ignored": true},
  "segments": [{
    "id": "seg_000000",
    "speaker": "spk_alice",
    "startMs": 1200,
    "endMs": 1800,
    "text": "hello there",
    "futureSegmentField": "ignored",
    "words": [
      {"id": "seg_000000:w_0", "text": "hello", "startMs": 1200, "endMs": 1450},
      {"id": "seg_000000:w_1", "text": "there", "startMs": 1500, "endMs": 1800}
    ]
  }]
}`
	segments, err := decodeTranscriptSegments(strings.NewReader(input))
	if err != nil {
		t.Fatalf("decodeTranscriptSegments: %v", err)
	}
	if len(segments) != 1 || segments[0].SpeakerID != "spk_alice" || segments[0].StartMS != 1200 || segments[0].EndMS != 1800 {
		t.Fatalf("segments = %#v", segments)
	}
	if len(segments[0].Words) != 2 || segments[0].Words[1].StartMS != 1500 || segments[0].Words[1].EndMS != 1800 {
		t.Fatalf("words = %#v", segments[0].Words)
	}
	if segments[0].Text != "" || segments[0].Words[0].Text != "" {
		t.Fatalf("waveform-only decoder retained transcript text: %#v", segments[0])
	}
}

func TestDecodeTranscriptSegmentsRejectsWrongVersionAndTrailingDocument(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "version", input: `{"version":"transcript.readable.v1","segments":[]}`, want: "unsupported transcript version"},
		{name: "trailing", input: `{"version":"transcript.words.v1","segments":[]} {}`, want: "unexpected data after transcript document"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeTranscriptSegments(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decodeTranscriptSegments() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRunFailureRemovesStaleOutput(t *testing.T) {
	tmp := t.TempDir()
	transcriptPath := filepath.Join(tmp, "transcript.words.v1.json")
	outputPath := filepath.Join(tmp, "challenges.v1.json")
	if err := os.WriteFile(transcriptPath, []byte(`{"version":"wrong","segments":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run(context.Background(), config{
		MKVPath:        filepath.Join(tmp, "missing.mkv"),
		TranscriptPath: transcriptPath,
		OutputPath:     outputPath,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "decode transcript") {
		t.Fatalf("run() error = %v, want transcript decode failure", err)
	}
	if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed rerun left stale output in place: %v", statErr)
	}
}

func TestRunRecordsExactSourceHashes(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	tmp := t.TempDir()
	mkvPath := filepath.Join(tmp, "meeting.mkv")
	cmd := exec.Command(ffmpeg,
		"-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=0.25",
		"-c:a", "libopus",
		mkvPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build MKV fixture: %v: %s", err, output)
	}

	transcriptPath := filepath.Join(tmp, "transcript.words.v1.json")
	transcriptBytes := []byte("{\n  \"version\": \"transcript.words.v1\",\n  \"segments\": []\n}\n")
	if err := os.WriteFile(transcriptPath, transcriptBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(tmp, "challenges.v1.json")
	if err := run(context.Background(), config{
		MKVPath:        mkvPath,
		TranscriptPath: transcriptPath,
		OutputPath:     outputPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	audioBytes, err := os.ReadFile(mkvPath)
	if err != nil {
		t.Fatal(err)
	}
	audioSum := sha256.Sum256(audioBytes)
	transcriptSum := sha256.Sum256(transcriptBytes)
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var sidecar struct {
		SourceAudio            string `json:"sourceAudio"`
		SourceAudioSHA256      string `json:"sourceAudioSha256"`
		SourceTranscript       string `json:"sourceTranscript"`
		SourceTranscriptSHA256 string `json:"sourceTranscriptSha256"`
	}
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if sidecar.SourceAudio != filepath.Base(mkvPath) || sidecar.SourceTranscript != filepath.Base(transcriptPath) {
		t.Fatalf("source names = %q/%q, want input basenames", sidecar.SourceAudio, sidecar.SourceTranscript)
	}
	if sidecar.SourceAudioSHA256 != fmtHash(audioSum) || sidecar.SourceTranscriptSHA256 != fmtHash(transcriptSum) {
		t.Fatalf("source hashes = %q/%q, want %s/%s", sidecar.SourceAudioSHA256, sidecar.SourceTranscriptSHA256, fmtHash(audioSum), fmtHash(transcriptSum))
	}
}

func fmtHash(sum [sha256.Size]byte) string {
	const hex = "0123456789abcdef"
	encoded := make([]byte, len(sum)*2)
	for i, value := range sum {
		encoded[i*2] = hex[value>>4]
		encoded[i*2+1] = hex[value&0x0f]
	}
	return string(encoded)
}
