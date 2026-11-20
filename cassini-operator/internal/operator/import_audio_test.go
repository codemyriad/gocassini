package operator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAudioTitleFromSourceDropsOnlyTheExtension(t *testing.T) {
	cases := map[string]string{
		"Board offsite.m4a":    "Board offsite",
		"/tmp/Weekly sync.mp3": "Weekly sync",
		"standup":              "standup",
		"  Q3 planning.wav  ":  "Q3 planning",
		"meeting.tar.gz":       "meeting.tar",
	}
	for in, want := range cases {
		if got := audioTitleFromSource(in); got != want {
			t.Fatalf("audioTitleFromSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeAudioTitleKeepsWordsAndRefusesPaths(t *testing.T) {
	cases := map[string]string{
		"Board offsite":          "Board offsite",
		"a/b\\c":                 "a b c",
		"  ..hidden..  ":         "hidden",
		"..":                     "Uploaded meeting",
		"   ":                    "Uploaded meeting",
		"line\nbreak":            "line break",
		strings.Repeat("x", 200): strings.Repeat("x", maxAudioTitleBytes),
	}
	for in, want := range cases {
		if got := sanitizeAudioTitle(in); got != want {
			t.Fatalf("sanitizeAudioTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMediaTimeoutForDurationHasAFloorAndCeiling(t *testing.T) {
	if got := mediaTimeoutForDuration(0, 10*time.Minute, 2*time.Hour); got != 10*time.Minute {
		t.Fatalf("zero duration timeout = %s", got)
	}
	if got := mediaTimeoutForDuration(30*60*1000, 10*time.Minute, 2*time.Hour); got != 70*time.Minute {
		t.Fatalf("30-minute media timeout = %s", got)
	}
	if got := mediaTimeoutForDuration(6*60*60*1000, 10*time.Minute, 2*time.Hour); got != 2*time.Hour {
		t.Fatalf("long media timeout = %s", got)
	}
}

// remuxAudioToMKV must make a Matroska file the build can probe from a normal
// upload container, and keep the meeting's title as the stream metadata.
func TestRemuxAudioToMKVProducesBuildInput(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.mp3")
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=0.4",
		"-c:a", "libmp3lame", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot write a test mp3: %v: %s", err, strings.TrimSpace(string(out)))
	}
	imp := &meetingImporter{}
	mkv, probe, err := imp.remuxAudioToMKV(context.Background(), src, dir, "Board offsite")
	if err != nil {
		t.Fatalf("remuxAudioToMKV() error = %v", err)
	}
	if filepath.Ext(mkv) != ".mkv" {
		t.Fatalf("output = %q, want an .mkv", mkv)
	}
	if info, err := os.Stat(mkv); err != nil || info.Size() == 0 {
		t.Fatalf("output missing or empty: %v", err)
	}
	if probe.DurationMS <= 0 || probe.Codec == "" {
		t.Fatalf("probe = %+v, want a duration and codec", probe)
	}
	streams, duration, err := probeMKVForTest(mkv)
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 || duration <= 0 || streams[0] != "flac" {
		t.Fatalf("build input streams=%v duration=%d", streams, duration)
	}
}

func probeMKVForTest(path string) ([]string, int64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "stream=codec_name:format=duration", "-of", "json", path).Output()
	if err != nil {
		return nil, 0, err
	}
	var probe struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, 0, err
	}
	var codecs []string
	for _, stream := range probe.Streams {
		codecs = append(codecs, stream.CodecName)
	}
	seconds, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return codecs, int64(seconds * 1000), nil
}

// buildAudioMeeting must call the ordinary build on the prepared MKV, write
// the output path it was given, and pass the operator's no-download policy.
func TestBuildAudioMeetingInvokesBuildAndWritesOutput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cassini")
	envFile := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != \"build\" ]; then exit 2; fi\n" +
		"printf '%s' \"$CASSINI_DISALLOW_MODEL_DOWNLOAD\" > " + envFile + "\n" +
		"printf 'portable meeting' > \"$4\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "meeting.opus")
	imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: bin}}
	if err := imp.buildAudioMeeting(context.Background(), filepath.Join(dir, "audio.mkv"), out, 0); err != nil {
		t.Fatalf("buildAudioMeeting() error = %v", err)
	}
	if body, err := os.ReadFile(envFile); err != nil || strings.TrimSpace(string(body)) != "1" {
		t.Fatalf("build environment = %q, %v; want CASSINI_DISALLOW_MODEL_DOWNLOAD=1", body, err)
	}
	if body, err := os.ReadFile(out); err != nil || string(body) != "portable meeting" {
		t.Fatalf("build output = %q, %v", body, err)
	}
}
