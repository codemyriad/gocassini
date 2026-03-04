package remux

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/pkg/core/session"
)

func TestResolveSessionJSONPathFromFileAndDir(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-a")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionJSON := filepath.Join(sessionDir, "session.json")
	if err := os.WriteFile(sessionJSON, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write session json: %v", err)
	}

	gotFile, err := ResolveSessionJSONPath(sessionJSON)
	if err != nil {
		t.Fatalf("resolve from file: %v", err)
	}
	if gotFile != sessionJSON {
		t.Fatalf("resolve file mismatch: got=%s want=%s", gotFile, sessionJSON)
	}

	gotDir, err := ResolveSessionJSONPath(sessionDir)
	if err != nil {
		t.Fatalf("resolve from dir: %v", err)
	}
	if gotDir != sessionJSON {
		t.Fatalf("resolve dir mismatch: got=%s want=%s", gotDir, sessionJSON)
	}
}

func TestResolveSessionJSONPathRejectsNonSessionFile(t *testing.T) {
	tmp := t.TempDir()
	notSession := filepath.Join(tmp, "input.json")
	if err := os.WriteFile(notSession, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := ResolveSessionJSONPath(notSession)
	if err == nil {
		t.Fatalf("expected resolve error for non-session file")
	}
}

func TestBuildFromSessionNoRemuxableStreams(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-b")
	streamsDir := filepath.Join(sessionDir, "streams")
	if err := os.MkdirAll(streamsDir, 0o755); err != nil {
		t.Fatalf("mkdir streams dir: %v", err)
	}

	sessionBody := `{
  "version": 1,
  "session_id": "s_test",
  "started_wall_utc": "2026-03-04T00:00:00Z",
  "started_mono_ns": 1,
  "platform": {
    "name": "nextcloudtalk",
    "deployment": "custom",
    "room": "room",
    "recorder_identity": {"display":"recorder","silent":true}
  },
  "packet_streams": [
    {"stream_id":"s_000001","ltid":"p:a:audio:mid","mid":"mid","rid":"","primary_ssrc":1,"codec":"audio/pcmu","clock_rate":8000,"start_mono_ns":1}
  ]
}`
	sessionJSON := filepath.Join(sessionDir, "session.json")
	if err := os.WriteFile(sessionJSON, []byte(sessionBody), 0o644); err != nil {
		t.Fatalf("write session json: %v", err)
	}

	_, err := BuildFromSession(sessionJSON, filepath.Join(tmp, "out.mkv"), BuildOptions{})
	if err == nil {
		t.Fatalf("expected build error")
	}
	if !strings.Contains(err.Error(), "no remuxable streams") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildStreamPlansIncludesTimelineAdjustments(t *testing.T) {
	segments := []segmentArtifact{
		{
			Stream:           sessionPacket("s_000001", "ltid-video", "video/vp8"),
			Kind:             "video",
			FirstNS:          10_000,
			FirstTimelineNS:  5_000,
			TimelineAdjustNS: -5_000,
			Packets:          1200,
			TimelineProfile: timelineProfile{
				Samples:             1200,
				RawDurationNS:       7_200_000_000,
				CorrectedDurationNS: 7_150_000_000,
			},
		},
		{
			Stream:           sessionPacket("s_000002", "ltid-audio", "audio/opus"),
			Kind:             "audio",
			FirstNS:          12_000,
			FirstTimelineNS:  12_000,
			TimelineAdjustNS: 0,
			Packets:          250,
			TimelineProfile: timelineProfile{
				Samples:             250,
				RawDurationNS:       6_000_000_000,
				CorrectedDurationNS: 6_000_000_000,
			},
		},
	}
	planned := []PlannedInput{
		{
			StreamInput: StreamInput{
				StreamID:        "s_000001",
				LTID:            "ltid-video",
				Kind:            "video",
				Codec:           "video/vp8",
				FirstRecvNS:     10_000,
				FirstTimelineNS: 5_000,
				SourceStart:     0.25,
			},
			OffsetSeconds: 1.25,
		},
		{
			StreamInput: StreamInput{
				StreamID:        "s_000002",
				LTID:            "ltid-audio",
				Kind:            "audio",
				Codec:           "audio/opus",
				FirstRecvNS:     12_000,
				FirstTimelineNS: 12_000,
				SourceStart:     0.10,
			},
			OffsetSeconds: 0.75,
		},
	}

	got := buildStreamPlans(segments, planned)
	if len(got) != 2 {
		t.Fatalf("stream plans len: got=%d want=2", len(got))
	}
	if got[0].StreamID != "s_000001" || got[0].TimelineAdjustNS != -5_000 {
		t.Fatalf("unexpected first stream plan: %+v", got[0])
	}
	if got[0].TimelineSamples != 1200 {
		t.Fatalf("expected samples=1200, got=%d", got[0].TimelineSamples)
	}
	if math.Abs(got[0].OffsetSeconds-1.25) > 1e-9 {
		t.Fatalf("unexpected first offset: %f", got[0].OffsetSeconds)
	}
	if got[1].StreamID != "s_000002" || got[1].TimelineAdjustNS != 0 {
		t.Fatalf("unexpected second stream plan: %+v", got[1])
	}
}

func sessionPacket(streamID, ltid, codec string) session.PacketStream {
	return session.PacketStream{
		StreamID: streamID,
		LTID:     ltid,
		Codec:    codec,
	}
}
