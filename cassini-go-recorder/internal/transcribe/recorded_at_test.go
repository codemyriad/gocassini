package transcribe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordedAtUTCFromStreams(t *testing.T) {
	anchored := func(wallMS, firstPacketMS int64) AudioStream {
		return AudioStream{
			FirstPacketTimeMS: firstPacketMS,
			TimeBase:          SourceTimeBase{FirstPacketWallMS: wallMS, FirstTimelineNS: 1, ClockRate: 48000, Known: true},
		}
	}
	originMS := time.Date(2026, 11, 9, 11, 31, 48, 250e6, time.UTC).UnixMilli()
	for _, tc := range []struct {
		name    string
		streams []AudioStream
		want    string
	}{
		{"no streams", nil, ""},
		{"no anchors (pre-2026-09 remux)", []AudioStream{{FirstPacketTimeMS: 0}, {FirstPacketTimeMS: 4000}}, ""},
		{"single track at origin", []AudioStream{anchored(originMS, 0)}, "2026-11-09T11:31:48.250Z"},
		// A late joiner's first packet is 12 s into the timeline, so its
		// arrival minus 12 s is the origin, not its arrival.
		{"late joiner only", []AudioStream{anchored(originMS+12000, 12000)}, "2026-11-09T11:31:48.250Z"},
		// The earliest anchored track decides; an unanchored earlier track
		// is skipped rather than read as the epoch.
		{"earliest anchored wins", []AudioStream{
			{FirstPacketTimeMS: 0},
			anchored(originMS+9001, 9000),
			anchored(originMS+3000, 3000),
		}, "2026-11-09T11:31:48.250Z"},
		// A video stream joined first, so even the earliest audio sits
		// after the origin.
		{"origin before any audio", []AudioStream{anchored(originMS+1500, 1500)}, "2026-11-09T11:31:48.250Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordedAtUTCFromStreams(tc.streams); got != tc.want {
				t.Fatalf("recordedAtUTCFromStreams = %q, want %q", got, tc.want)
			}
		})
	}
}

// buildTalkShapedMeeting writes an MKV tagged the way pkg/core/remux writes a
// Talk recording: session_started_at a few seconds before any media, each
// audio track anchored with first_packet_wall_ms/first_timeline_ns, the
// second participant joining lateSeconds in, and the legacy recorded_at_local
// label (UTC digits) that v0.2.0+ MKVs carry.
func buildTalkShapedMeeting(t *testing.T, dir string, origin time.Time, lateSeconds float64) string {
	t.Helper()
	sessionStarted := origin.Add(-3 * time.Second)
	monoOrigin := origin.UnixNano()
	lateMS := int64(lateSeconds * 1000)
	outPath := filepath.Join(dir, "recording.mkv")
	if err := runMediaCommand("ffmpeg",
		"-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=500:sample_rate=48000:duration=0.5",
		"-itsoffset", fmt.Sprintf("%.3f", lateSeconds),
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=0.5",
		"-map", "0:a:0", "-map", "1:a:0",
		"-metadata", "session_started_at="+sessionStarted.UTC().Format(time.RFC3339Nano),
		"-metadata", "recorded_at_local="+origin.UTC().Format("2006-01-02T15:04:05"),
		"-metadata:s:a:0", "participant_id=user-a",
		"-metadata:s:a:0", fmt.Sprintf("first_packet_wall_ms=%d", origin.UnixMilli()),
		"-metadata:s:a:0", fmt.Sprintf("first_timeline_ns=%d", monoOrigin),
		"-metadata:s:a:0", "clock_rate=48000",
		"-metadata:s:a:1", "participant_id=user-b",
		"-metadata:s:a:1", fmt.Sprintf("first_packet_wall_ms=%d", origin.UnixMilli()+lateMS),
		"-metadata:s:a:1", fmt.Sprintf("first_timeline_ns=%d", monoOrigin+lateMS*1e6),
		"-metadata:s:a:1", "clock_rate=48000",
		"-c:a", "libopus",
		outPath,
	); err != nil {
		t.Fatalf("create talk-shaped meeting: %v", err)
	}
	return outPath
}

func TestProbeTalkShapedMKVYieldsRecordedAtUTCNotLocal(t *testing.T) {
	requireFFMediaTools(t)
	origin := time.Date(2026, 11, 9, 11, 31, 48, 250e6, time.UTC)
	mkv := buildTalkShapedMeeting(t, t.TempDir(), origin, 4)

	_, _, recordedAtUTC, err := probeMKV(mkv)
	if err != nil {
		t.Fatal(err)
	}
	// Position 0 is the first packet, not session_started_at (3 s earlier).
	if recordedAtUTC != "2026-11-09T11:31:48.250Z" {
		t.Fatalf("recordedAtUtc = %q, want the first packet's instant 2026-11-09T11:31:48.250Z", recordedAtUTC)
	}

	// Through the untranscribed build, which writes the bundle manifest.
	out := t.TempDir()
	if err := BuildMeetingArtifact(t.Context(), mkv, out, BuildConfig{TranscriptionMode: "off"}, nopWriter{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest artifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Source.RecordedAtUTC != "2026-11-09T11:31:48.250Z" {
		t.Fatalf("bundle lost recordedAtUtc: %s", raw)
	}
	if manifest.Source.RecordedAtLocal != "" {
		t.Fatalf("zone-unknown recordedAtLocal propagated: %s", raw)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestWriteManifestRejectsMalformedRecordedAtUTC(t *testing.T) {
	for _, value := range []string{"2026-11-09T12:31:48+01:00", "2026-11-09T12:31:48", "2026-11-09 12:31:48Z", "2026-13-09T12:31:48Z"} {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := WriteManifest(path, ManifestInput{SrcBasename: "recording.mkv", RecordedAtUTC: value}); err == nil {
			t.Fatalf("WriteManifest accepted recordedAtUtc %q", value)
		}
	}
}
