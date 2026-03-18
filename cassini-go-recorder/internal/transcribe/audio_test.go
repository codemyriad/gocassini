package transcribe

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExtractSpeakerFloatsPreservesDelayedTrackStart(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	mkvPath := buildDelayedTrack(t, tmp, "speaker-a", 10.0)

	streams, sourceDurationMS, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatalf("probe delayed mkv: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}

	samples, err := ExtractSpeakerFloats(mkvPath, streams[0])
	if err != nil {
		t.Fatalf("extract delayed speaker floats: %v", err)
	}

	actualDurationMS := int64(len(samples)) * 1000 / 16000
	if deltaMS(sourceDurationMS, actualDurationMS) > 1500 {
		t.Fatalf("delayed speaker timeline collapsed: source=%dms extracted=%dms", sourceDurationMS, actualDurationMS)
	}
}

func TestMixDownToWebMPreservesDelayedTrackOffsets(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	trackA := buildDelayedTrack(t, tmp, "speaker-a", 0.0)
	trackB := buildDelayedTrack(t, tmp, "speaker-b", 10.0)
	meetingPath := mergeAudioTracks(t, tmp, trackA, trackB)

	streams, sourceDurationMS, err := ProbeMKV(meetingPath)
	if err != nil {
		t.Fatalf("probe delayed meeting mkv: %v", err)
	}

	outPath := filepath.Join(tmp, "meeting.webm")
	if err := MixDownToWebM(meetingPath, streams, outPath); err != nil {
		t.Fatalf("mix delayed meeting: %v", err)
	}

	mixedDurationMS, err := AudioDurationMS(outPath)
	if err != nil {
		t.Fatalf("probe mixed webm duration: %v", err)
	}
	if deltaMS(sourceDurationMS, mixedDurationMS) > 1500 {
		t.Fatalf("delayed mix timeline collapsed: source=%dms mixed=%dms", sourceDurationMS, mixedDurationMS)
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

func buildDelayedTrack(t *testing.T, dir string, name string, offsetSeconds float64) string {
	t.Helper()

	basePath := filepath.Join(dir, name+"-base.mkv")
	outPath := filepath.Join(dir, name+".mkv")
	if err := runMediaCommand(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=1000:sample_rate=48000:duration=0.1",
		"-c:a", "libopus",
		basePath,
	); err != nil {
		t.Fatalf("create base delayed track: %v", err)
	}
	if err := runMediaCommand(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-itsoffset", fmt.Sprintf("%.3f", offsetSeconds),
		"-i", basePath,
		"-map", "0:a:0",
		"-c", "copy",
		outPath,
	); err != nil {
		t.Fatalf("shift delayed track: %v", err)
	}
	return outPath
}

func mergeAudioTracks(t *testing.T, dir string, tracks ...string) string {
	t.Helper()

	outPath := filepath.Join(dir, "meeting.mkv")
	args := []string{"-y", "-v", "error"}
	for _, track := range tracks {
		args = append(args, "-i", track)
	}
	for i := range tracks {
		args = append(args, "-map", fmt.Sprintf("%d:a:0", i))
	}
	args = append(args, "-c", "copy", outPath)
	if err := runMediaCommand("ffmpeg", args...); err != nil {
		t.Fatalf("merge delayed tracks: %v", err)
	}
	return outPath
}

func runMediaCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, string(output))
	}
	return nil
}

func deltaMS(a int64, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
