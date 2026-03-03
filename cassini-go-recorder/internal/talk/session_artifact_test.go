package talk

import (
	"os"
	"path/filepath"
	"testing"
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
