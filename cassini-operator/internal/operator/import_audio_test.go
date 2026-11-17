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

// fakeAudioBuild is a stand-in `cassini build` that records each run's
// environment and arguments under dir and writes the output it was given.
// While dir/block exists it waits, so a test can act on a running build.
func fakeAudioBuild(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "cassini")
	script := "#!/bin/sh\n" +
		"n=$(ls " + dir + " | grep -c '^run-')\n" +
		"env > " + dir + "/run-$n.env\n" +
		"echo \"$@\" > " + dir + "/args-$n.txt\n" +
		"while [ -e " + dir + "/block ]; do sleep 0.05; done\n" +
		"printf 'portable meeting' > \"$4\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func fileEnvValue(t *testing.T, path, key string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

// The audio build runs under the resource governor a Talk build runs under:
// the device and the thread budget are pinned for the child.
func TestBuildAudioMeetingAppliesTheResourceGovernor(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	dir := t.TempDir()
	imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: fakeAudioBuild(t, dir), rt: rt}}
	out := filepath.Join(dir, "meeting.opus")
	if err := imp.buildAudioMeeting(context.Background(), filepath.Join(dir, "audio.mkv"), out, 0); err != nil {
		t.Fatalf("buildAudioMeeting() error = %v", err)
	}
	envPath := filepath.Join(dir, "run-0.env")
	if got := fileEnvValue(t, envPath, envSTTDevice); got == "" {
		t.Fatalf("%s unset: the build would pick a device on its own", envSTTDevice)
	}
	want := strconv.Itoa(resourceLimitsFromEnv().threadBudget())
	if got := fileEnvValue(t, envPath, envSTTNumThreads); got != want {
		t.Fatalf("%s = %q, want the operator's thread budget %s", envSTTNumThreads, got, want)
	}
	if got := fileEnvValue(t, envPath, envDisallowModelDownload); got != "1" {
		t.Fatalf("%s = %q, want 1", envDisallowModelDownload, got)
	}
}

// A recording that takes the host stops the audio build, and the build starts
// again when the host is free, instead of failing the upload.
func TestBuildAudioMeetingYieldsToARecordingAndResumes(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	dir := t.TempDir()
	block := filepath.Join(dir, "block")
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: fakeAudioBuild(t, dir), rt: rt}}
	out := filepath.Join(dir, "meeting.opus")
	done := make(chan error, 1)
	go func() {
		done <- imp.buildAudioMeeting(context.Background(), filepath.Join(dir, "audio.mkv"), out, 0)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "run-0.env")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first build never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// What reserveRecordSlot does when a capture needs the host.
	rt.priorityMu.Lock()
	cancel := rt.priorityBuildCancel
	rt.priorityMu.Unlock()
	if cancel == nil {
		t.Fatal("the audio build did not register as a background build")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	cancel(errRecordingPriority)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("buildAudioMeeting() error = %v, want the build to resume", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("buildAudioMeeting() did not finish")
	}
	if _, err := os.Stat(filepath.Join(dir, "run-1.env")); err != nil {
		t.Fatalf("no second build after the yield: %v", err)
	}
}

// A cancel reaches an audio import that is still waiting for the build gate.
func TestBuildAudioMeetingCancelWhileWaitingForTheGate(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	dir := t.TempDir()
	imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: fakeAudioBuild(t, dir), rt: rt}}
	rt.buildExecutionMu.Lock()
	defer rt.buildExecutionMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- imp.buildAudioMeeting(ctx, filepath.Join(dir, "audio.mkv"), filepath.Join(dir, "meeting.opus"), 0)
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("buildAudioMeeting() succeeded while the gate was held")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not reach an import waiting for the build gate")
	}
	if _, err := os.Stat(filepath.Join(dir, "run-0.env")); err == nil {
		t.Fatal("the build ran although the import was canceled")
	}
}

// A build that crashes while transcribing is run again without transcription,
// so the upload still becomes a meeting and says why it has no transcript.
func TestRunAudioBuildFallsBackToAudioOnly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cassini")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + dir + "/calls.txt\n" +
		"if [ \"$5\" != \"--transcription\" ]; then echo 'cuDNN failure' >&2; exit 134; fi\n" +
		"printf '%s' \"$CASSINI_TRANSCRIPTION_REASON\" > " + dir + "/reason.txt\n" +
		"printf 'portable meeting' > \"$4\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	imp := &meetingImporter{sink: &nextcloudFilesPublishSink{cassiniBin: bin}}
	out := filepath.Join(dir, "meeting.opus")
	fallback := []string{"CASSINI_TRANSCRIPTION=off", "CASSINI_TRANSCRIPTION_REASON=" + transcriptionReasonFailed}
	if err := imp.runAudioBuild(context.Background(), []string{"CASSINI_TRANSCRIPTION=on"}, filepath.Join(dir, "audio.mkv"), out, 0, fallback); err != nil {
		t.Fatalf("runAudioBuild() error = %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "reason.txt")); string(body) != transcriptionReasonFailed {
		t.Fatalf("fallback reason = %q, want %q", body, transcriptionReasonFailed)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "calls.txt")); strings.Count(string(body), "\n") != 2 {
		t.Fatalf("builds run = %q, want the failed one and the audio-only one", body)
	}
}
