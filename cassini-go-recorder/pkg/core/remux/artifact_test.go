package remux

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
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

	sess := session.Session{
		LogicalTracks: []session.LogicalTrack{
			{LTID: "ltid-video", Kind: "video", ParticipantID: "alice", MID: "0"},
			{LTID: "ltid-audio", Kind: "audio", ParticipantID: "alice", MID: "1"},
		},
		Participants: []session.Participant{
			{PID: "alice", Display: "Alice"},
		},
	}
	got := buildStreamPlans(sess, segments, planned)
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
	if got[0].ParticipantID != "alice" || got[0].ParticipantName != "Alice" {
		t.Fatalf("missing participant metadata in plan: %+v", got[0])
	}
	if got[0].MID != "" || got[1].MID != "" {
		t.Fatalf("unexpected MID values without stream metadata: video=%q audio=%q", got[0].MID, got[1].MID)
	}
}

func TestMergeSegmentsEmbedsCassiniMetadata(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	tmp := t.TempDir()
	inputPath := filepath.Join(tmp, "input.mkv")
	if err := runCommand(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "anullsrc=r=48000:cl=stereo",
		"-t", "0.1",
		"-c:a", "libopus",
		inputPath,
	); err != nil {
		t.Fatalf("create input mkv: %v", err)
	}

	sess := session.Session{
		Version:        1,
		SessionID:      "meeting_20260310T120000Z",
		StartedWallUTC: "2026-03-10T12:00:00Z",
		Platform: session.Platform{
			Name:       "nextcloudtalk",
			Deployment: "custom",
			RecorderIdentity: session.RecorderIdentity{
				Display: "Recorder",
				Silent:  true,
			},
		},
		Participants: []session.Participant{
			{PID: "silviot", Display: "Silvio"},
		},
		LogicalTracks: []session.LogicalTrack{
			{
				LTID:          "p:silviot:audio:janus",
				Kind:          "audio",
				Source:        "janus",
				ParticipantID: "silviot",
				MID:           "0",
			},
		},
		PacketStreams: []session.PacketStream{
			{
				StreamID:    "s_000001",
				LTID:        "p:silviot:audio:janus",
				MID:         "0",
				PrimarySSRC: 1234,
				Codec:       "audio/opus",
				ClockRate:   48000,
				PT:          111,
			},
		},
	}
	segments := []segmentArtifact{
		{
			Stream:           sess.PacketStreams[0],
			Kind:             "audio",
			TempPath:         inputPath,
			FirstNS:          1_000,
			FirstTimelineNS:  251_000,
			TimelineAdjustNS: 250_000,
			Packets:          12,
			TimelineProfile: timelineProfile{
				Samples:             12,
				RawDurationNS:       100_000_000,
				CorrectedDurationNS: 100_250_000,
			},
		},
	}
	plans := []StreamPlan{
		{
			StreamID:               "s_000001",
			LTID:                   "p:silviot:audio:janus",
			Kind:                   "audio",
			Codec:                  "audio/opus",
			ParticipantID:          "silviot",
			ParticipantName:        "Silvio",
			MID:                    "0",
			ClockRate:              48000,
			PT:                     111,
			RTPPackets:             12,
			FirstRecvNS:            1_000,
			FirstTimelineNS:        251_000,
			TimelineAdjustNS:       250_000,
			TimelineSamples:        12,
			TimelineRawDurationNS:  100_000_000,
			TimelineCorrDurationNS: 100_250_000,
			SourceStartSeconds:     0.0,
			OffsetSeconds:          0.0,
		},
	}

	outputPath := filepath.Join(tmp, "out.mkv")
	if err := mergeSegments(outputPath, "Cassini Test Meeting", sess, segments, plans, tmp); err != nil {
		t.Fatalf("merge segments: %v", err)
	}

	raw, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format_tags:stream=index,codec_type:stream_tags",
		"-of", "json",
		outputPath,
	).Output()
	if err != nil {
		t.Fatalf("ffprobe output: %v", err)
	}

	var meta struct {
		Streams []struct {
			Index     int               `json:"index"`
			CodecType string            `json:"codec_type"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse ffprobe json: %v", err)
	}

	if got := tagValue(meta.Format.Tags, "SESSION_ID", "session_id"); got != sess.SessionID {
		t.Fatalf("session_id tag mismatch: got=%q want=%q", got, sess.SessionID)
	}
	if got := tagValue(meta.Format.Tags, "CASSINI_FORMAT", "cassini_format"); got != MeetingFormatVersion {
		t.Fatalf("cassini_format tag mismatch: got=%q want=%q", got, MeetingFormatVersion)
	}
	if got := tagValue(meta.Format.Tags, "CASSINI_EMBEDDED_REPORT", "cassini_embedded_report"); got != embeddedReportFile {
		t.Fatalf("embedded report tag mismatch: got=%q want=%q", got, embeddedReportFile)
	}

	var audioTags map[string]string
	var attachmentTags map[string]string
	for _, stream := range meta.Streams {
		switch stream.CodecType {
		case "audio":
			audioTags = stream.Tags
		case "attachment":
			attachmentTags = stream.Tags
		}
	}
	if audioTags == nil {
		t.Fatalf("expected audio stream metadata")
	}
	if tagValue(audioTags, "STREAM_ID", "stream_id") != "s_000001" {
		t.Fatalf("stream_id tag missing from audio stream: %v", audioTags)
	}
	if tagValue(audioTags, "OFFSET_SECONDS", "offset_seconds") != "0.000000" {
		t.Fatalf("offset_seconds tag mismatch: %v", audioTags)
	}
	if tagValue(audioTags, "TIMELINE_ADJUST_NS", "timeline_adjust_ns") != "250000" {
		t.Fatalf("timeline_adjust_ns tag mismatch: %v", audioTags)
	}
	if tagValue(audioTags, "PARTICIPANT_NAME", "participant_name") != "Silvio" {
		t.Fatalf("participant_name tag mismatch: %v", audioTags)
	}
	if tagValue(attachmentTags, "FILENAME", "filename") != embeddedReportFile {
		t.Fatalf("attachment filename mismatch: %v", attachmentTags)
	}
	if tagValue(attachmentTags, "MIMETYPE", "mimetype") != "application/json" {
		t.Fatalf("attachment mimetype mismatch: %v", attachmentTags)
	}

	reportBody, err := os.ReadFile(filepath.Join(tmp, embeddedReportFile))
	if err != nil {
		t.Fatalf("read embedded report source: %v", err)
	}
	var report embeddedReport
	if err := json.Unmarshal(reportBody, &report); err != nil {
		t.Fatalf("parse embedded report json: %v", err)
	}
	if report.Schema != embeddedReportSchema {
		t.Fatalf("unexpected embedded report schema: %q", report.Schema)
	}
	if report.Session.SessionID != sess.SessionID {
		t.Fatalf("unexpected embedded report session id: %q", report.Session.SessionID)
	}
	if len(report.Artifact.StreamPlans) != 1 || report.Artifact.StreamPlans[0].StreamID != "s_000001" {
		t.Fatalf("unexpected embedded report plans: %+v", report.Artifact.StreamPlans)
	}
}

func sessionPacket(streamID, ltid, codec string) session.PacketStream {
	return session.PacketStream{
		StreamID: streamID,
		LTID:     ltid,
		Codec:    codec,
	}
}

func tagValue(tags map[string]string, keys ...string) string {
	for _, key := range keys {
		if value, ok := tags[key]; ok && value != "" {
			return value
		}
	}
	return ""
}
