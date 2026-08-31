package transcribe

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestProbeFirstPacketTimeMSIgnoresSuccessfulFFprobeWarning(t *testing.T) {
	binDir := t.TempDir()
	ffprobe := filepath.Join(binDir, "ffprobe")
	script := `#!/bin/sh
printf '%s\n' '[matroska,webm @ 0x1] File ended prematurely' >&2
printf '%s\n' '12.345000'
`
	if err := os.WriteFile(ffprobe, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := probeFirstPacketTimeMS("truncated-but-readable.mkv", 6)
	if err != nil {
		t.Fatalf("probe first packet with stderr warning: %v", err)
	}
	if got != 12_345 {
		t.Fatalf("first packet timestamp = %dms, want 12345ms", got)
	}
}

func TestExtractSpeakerFloatsAppliesPacketOffsetExactlyOnce(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	mkvPath := buildOffsetMeeting(t, tmp, 10.0)

	streams, sourceDurationMS, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatalf("probe delayed mkv: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(streams))
	}
	if got := streams[1].FirstPacketTimeMS; got < 9700 || got > 10300 {
		t.Fatalf("first packet timestamp = %dms, want about 10000ms", got)
	}

	samples, err := ExtractSpeakerFloats(mkvPath, streams[1])
	if err != nil {
		t.Fatalf("extract delayed speaker floats: %v", err)
	}

	actualDurationMS := int64(len(samples)) * 1000 / 16000
	if deltaMS(sourceDurationMS, actualDurationMS) > 250 {
		t.Fatalf("delayed speaker timeline collapsed or doubled: source=%dms extracted=%dms", sourceDurationMS, actualDurationMS)
	}
	firstSignalMS := firstSignalSample(samples, 0.01) * 1000 / 16000
	if firstSignalMS < 9700 || firstSignalMS > 10300 {
		t.Fatalf("delayed speaker signal starts at %dms, want about 10000ms", firstSignalMS)
	}
}

func TestSparseTimelineDecodeRebasesVeryLateFirstPacket(t *testing.T) {
	requireFFMediaTools(t)

	// Both field failures that motivated this regression had late stereo Opus
	// tracks with ordinary WebRTC timestamp jitter. This fully synthetic shape
	// reproduces exit 139 in libswresample 3.9.100 (FFmpeg 4.4.2); a constant-PTS
	// late sine wave does not. The MKV stays tiny because the 3,637-second offset
	// lives in packet timestamps rather than encoded silence.
	mkvPath := buildSparseJitterMeeting(t, t.TempDir())
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatalf("probe very-late stream: %v", err)
	}
	if got := streams[1].FirstPacketTimeMS; got < 3_637_100 || got > 3_637_800 {
		t.Fatalf("first packet timestamp = %dms, want about 3637463ms", got)
	}

	args := []string{"-y", "-v", "error", "-i", mkvPath}
	args = append(args, sparseTimelineDecodeArgs(streams[1], 16000)...)
	args = append(args, "-f", "null", "-")
	if err := runMediaCommand("ffmpeg", args...); err != nil {
		t.Fatalf("decode very-late sparse stream: %v", err)
	}
}

func TestMixDownToWebMPreservesDelayedTrackOffsets(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	meetingPath := buildOffsetMeeting(t, tmp, 10.0)

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
	if deltaMS(sourceDurationMS, mixedDurationMS) > 500 {
		t.Fatalf("delayed mix timeline collapsed or doubled: source=%dms mixed=%dms", sourceDurationMS, mixedDurationMS)
	}
}

func TestProbeMKVPrefersParticipantMetadataAndKeepsLegacyFallback(t *testing.T) {
	requireFFMediaTools(t)

	mkvPath := buildParticipantMetadataMeeting(t, t.TempDir())
	streams, _, err := ProbeMKV(mkvPath)
	if err != nil {
		t.Fatalf("ProbeMKV: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("expected 2 audio streams, got %d", len(streams))
	}

	if got := streams[0]; got.ParticipantID != "user-alice" || got.SpeakerLabel != "Alice Canonical" || got.SpeakerID != "spk_user_alice_0e7b8c3e3b7f94ed81538a56" {
		t.Fatalf("participant-tagged stream = %#v, want participant user-alice, label Alice Canonical, stable hashed speaker ID", got)
	}
	if got := streams[1]; got.ParticipantID != "" || got.SpeakerLabel != "Legacy Bob" || got.SpeakerID != "spk_legacy_bob_42651a3b862b81c2d596dac6" {
		t.Fatalf("legacy stream = %#v, want empty participant, label Legacy Bob, stable hashed speaker ID", got)
	}
}

func TestSpeakerIDFromLabelSeparatesSanitizationCollisions(t *testing.T) {
	forwardSlash := speakerIDFromLabel("alpha/beta")
	backslash := speakerIDFromLabel(`alpha\beta`)
	if forwardSlash == backslash {
		t.Fatalf("lossy slugs collided: %q", forwardSlash)
	}
	if !strings.HasPrefix(forwardSlash, "spk_alpha_beta_") || !strings.HasPrefix(backslash, "spk_alpha_beta_") {
		t.Fatalf("speaker IDs lost readable slug: %q, %q", forwardSlash, backslash)
	}
	if got := speakerIDFromLabel("alpha/beta"); got != forwardSlash {
		t.Fatalf("speaker ID is not deterministic: first=%q second=%q", forwardSlash, got)
	}
}

func TestSpeakerIDFromLabelHandlesNonASCIIWithoutColliding(t *testing.T) {
	if got, want := speakerIDFromLabel("東京"), "spk_unknown_130016b2599bf7e5978cae78"; got != want {
		t.Fatalf("non-ASCII speaker ID = %q, want %q", got, want)
	}
	if got, want := speakerIDFromLabel("José 東京"), "spk_jos_d7af912635f816d90ab8779d"; got != want {
		t.Fatalf("mixed-script speaker ID = %q, want %q", got, want)
	}
	if speakerIDFromLabel("東京") == speakerIDFromLabel("大阪") {
		t.Fatal("distinct non-ASCII identities collided")
	}
}

func TestReadPCM16LEFloatsHandlesShortReads(t *testing.T) {
	raw := []byte{
		0x00, 0x00, // 0
		0xff, 0x7f, // 32767
		0x00, 0x80, // -32768
		0x00, 0x40, // 16384
	}
	got, err := readPCM16LEFloats(iotest.OneByteReader(bytes.NewReader(raw)), len(raw)/2)
	if err != nil {
		t.Fatalf("read chunked PCM: %v", err)
	}
	want := []float32{0, 32767.0 / 32768.0, -1, 0.5}
	if len(got) != len(want) {
		t.Fatalf("sample count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-7 {
			t.Errorf("sample %d = %f, want %f", i, got[i], want[i])
		}
	}
}

func TestReadPCM16LEFloatsRejectsPartialSample(t *testing.T) {
	if _, err := readPCM16LEFloats(bytes.NewReader([]byte{0x01}), 0); err == nil {
		t.Fatal("expected odd-byte PCM error")
	}
}

func TestSetPCMCapacityDurationHintsUsesPlayableMixWithoutChangingTiming(t *testing.T) {
	streams := []AudioStream{
		{
			Index:              7,
			ParticipantID:      "alice",
			SpeakerID:          "spk_alice",
			SpeakerLabel:       "Alice",
			Channels:           2,
			StartTimeMS:        12_345,
			FirstPacketTimeMS:  12_678,
			TimelineDurationMS: 1_977_527,
		},
	}

	setPCMCapacityDurationHints(streams, 242_413)

	got := streams[0]
	if got.TimelineDurationMS != 242_413 {
		t.Fatalf("PCM capacity duration = %d, want playable duration 242413", got.TimelineDurationMS)
	}
	if got.Index != 7 || got.ParticipantID != "alice" || got.SpeakerID != "spk_alice" ||
		got.SpeakerLabel != "Alice" || got.Channels != 2 || got.StartTimeMS != 12_345 || got.FirstPacketTimeMS != 12_678 {
		t.Fatalf("non-capacity stream metadata changed: %#v", got)
	}
	if newCapacity, oldCapacity := expectedPCMSamples(got.TimelineDurationMS, 16000), expectedPCMSamples(1_977_527, 16000); newCapacity >= oldCapacity {
		t.Fatalf("playable-duration capacity %d did not reduce overstated source capacity %d", newCapacity, oldCapacity)
	}
}

func TestReadPCM16LEFloatsDoesNotTruncateWhenDurationHintIsTooShort(t *testing.T) {
	// A malformed or truncated duration is only a capacity hint. Supply more
	// than its one-second allowance and verify the decoder grows instead of
	// discarding playable PCM.
	hint := expectedPCMSamples(1, 16000)
	sampleCount := hint + 1234
	raw := make([]byte, sampleCount*2)
	raw[len(raw)-2], raw[len(raw)-1] = 0xff, 0x7f

	got, err := readPCM16LEFloats(bytes.NewReader(raw), hint)
	if err != nil {
		t.Fatalf("read PCM beyond undersized duration hint: %v", err)
	}
	if len(got) != sampleCount {
		t.Fatalf("sample count = %d, want %d (duration hint must not truncate)", len(got), sampleCount)
	}
	if math.Abs(float64(got[len(got)-1]-32767.0/32768.0)) > 1e-7 {
		t.Fatalf("last sample = %f, want decoded tail sample", got[len(got)-1])
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

func buildOffsetMeeting(t *testing.T, dir string, offsetSeconds float64) string {
	t.Helper()

	outPath := filepath.Join(dir, "meeting.mkv")
	if err := runMediaCommand(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=500:sample_rate=48000:duration=0.1",
		"-itsoffset", fmt.Sprintf("%.3f", offsetSeconds),
		"-f", "lavfi",
		"-i", "sine=frequency=1000:sample_rate=48000:duration=0.1",
		"-map", "0:a:0",
		"-map", "1:a:0",
		"-metadata:s:a:0", "title=speaker-a",
		"-metadata:s:a:1", "title=speaker-b",
		"-c:a", "libopus",
		outPath,
	); err != nil {
		t.Fatalf("create offset meeting: %v", err)
	}
	return outPath
}

func buildSparseJitterMeeting(t *testing.T, dir string) string {
	t.Helper()

	outPath := filepath.Join(dir, "sparse-jitter-meeting.mkv")
	if err := runMediaCommand(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-copyts",
		"-f", "lavfi",
		"-i", "sine=frequency=500:sample_rate=48000:duration=0.1",
		"-f", "lavfi",
		"-i", "anullsrc=r=48000:cl=stereo:d=530,asetnsamples=n=960:p=1,asetpts=PTS+3637.463/TB+(random(0)-0.5)*1920",
		"-map", "0:a:0",
		"-map", "1:a:0",
		"-c:a", "libopus",
		"-b:a", "8k",
		"-avoid_negative_ts", "disabled",
		outPath,
	); err != nil {
		t.Fatalf("create sparse jitter meeting: %v", err)
	}
	return outPath
}

func buildParticipantMetadataMeeting(t *testing.T, dir string) string {
	t.Helper()

	outPath := filepath.Join(dir, "participant-metadata.mkv")
	if err := runMediaCommand(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=500:sample_rate=48000:duration=0.1",
		"-f", "lavfi",
		"-i", "sine=frequency=1000:sample_rate=48000:duration=0.1",
		"-map", "0:a:0",
		"-map", "1:a:0",
		"-metadata:s:a:0", "title=Legacy Alice",
		"-metadata:s:a:0", "participant_id=user-alice",
		"-metadata:s:a:0", "participant_name=Alice Canonical",
		"-metadata:s:a:1", "title=Legacy Bob",
		"-c:a", "libopus",
		outPath,
	); err != nil {
		t.Fatalf("create participant metadata meeting: %v", err)
	}
	return outPath
}

func firstSignalSample(samples []float32, threshold float32) int64 {
	for i, sample := range samples {
		if sample > threshold || sample < -threshold {
			return int64(i)
		}
	}
	return -1
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
