package talk

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocassini/pkg/core/session"
	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
)

func TestSessionArtifactBootAndClose(t *testing.T) {
	tmp := t.TempDir()
	finalOutput := filepath.Join(tmp, "meeting.mkv")
	artifact, err := newSessionCaptureArtifact(finalOutput, "https://example.test/call/room", "room-token", "recorder")
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if artifact == nil {
		t.Fatalf("expected artifact")
	}

	if _, err := os.Stat(artifact.sessionPath); err != nil {
		t.Fatalf("session metadata missing: %v", err)
	}
	if _, err := os.Stat(artifact.eventsPath); err != nil {
		t.Fatalf("events log missing: %v", err)
	}
	summary := artifact.summary()
	if !summary.Enabled {
		t.Fatal("expected artifact enabled")
	}

	if err := artifact.close(); err != nil {
		t.Fatalf("close artifact: %v", err)
	}
}

func TestSessionArtifactHelpers(t *testing.T) {
	if got := sanitizeSessionPathPart("a/b\\c"); got != "a_b_c" {
		t.Fatalf("sanitize session path part: got=%q", got)
	}
	if got := parseFmtp("maxplaybackrate=64000;stereo=1"); got["maxplaybackrate"] != "64000" || got["stereo"] != "1" {
		t.Fatalf("parseFmtp got=%v", got)
	}
	if got := logicalTrackKey("sid-1", "Video", "stream", ""); got == "" {
		t.Fatalf("expected logicalTrackKey")
	}
	if got := inferSource("audio"); got != "audio" {
		t.Fatalf("inferSource audio: got=%q", got)
	}
	if got := inferSource("video"); got != "camera" {
		t.Fatalf("inferSource video: got=%q", got)
	}
}

func TestSessionArtifactStreamCloseBuildsIndex(t *testing.T) {
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "recording.mkv")
	artifact, err := newSessionCaptureArtifact(artifactPath, "https://example.test/call/room", "room-token", "recorder")
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	if _, err := os.Stat(artifact.eventsPath); err != nil {
		t.Fatalf("expected events file: %v", err)
	}

	streamID := "s_000001"
	streamPath := filepath.Join(artifact.streamsDir, streamID+".rtplog")
	header := store.StreamHeader{
		StreamID:    streamID,
		Codec:       "audio/opus",
		ClockRate:   48000,
		Direction:   "recvonly",
		StartMonoNS: uint64(time.Now().UnixNano()),
	}
	w, err := store.NewWriter(streamPath, header)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	artifact.streams[streamID] = &sessionCaptureStream{
		stream: session.PacketStream{
			StreamID: streamID,
		},
		writer:    w,
		logPath:   streamPath,
		indexPath: streamPath + ".idx",
	}
	artifact.mu.Lock()
	artifact.sessionMeta.PacketStreams = append(artifact.sessionMeta.PacketStreams, session.PacketStream{
		StreamID: streamID,
	})
	artifact.mu.Unlock()

	pkt := &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: 1,
			Timestamp:      1000,
			SSRC:           1234,
		},
		Payload: []byte{0x01, 0x02},
	}
	if err := artifact.writeRTP(streamID, pkt, time.Now()); err != nil {
		t.Fatalf("write packet: %v", err)
	}
	if err := artifact.closeStream(streamID, "test", time.Now()); err != nil {
		t.Fatalf("close stream: %v", err)
	}

	reader, err := store.OpenReader(streamPath)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if _, err := reader.Next(); err != nil {
		t.Fatalf("read first record: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}

	if _, err := os.Stat(streamPath + ".idx"); err != nil {
		t.Fatalf("expected idx file: %v", err)
	}

	summary := artifact.summary()
	if summary.ActiveStreamCount != 0 {
		t.Fatalf("expected no active streams, got=%d", summary.ActiveStreamCount)
	}
	if summary.PacketCount != 1 {
		t.Fatalf("expected one packet, got=%d", summary.PacketCount)
	}

	data, err := os.ReadFile(artifact.eventsPath)
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("expected session event log output")
	}
}
