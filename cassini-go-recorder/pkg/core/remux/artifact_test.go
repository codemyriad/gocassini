package remux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
