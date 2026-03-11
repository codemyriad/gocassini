package cassini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRunBundleUsesBundleLocalRecordingPath(t *testing.T) {
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "demo.run")

	bundle, err := PrepareRunBundle(outDir, false)
	if err != nil {
		t.Fatalf("prepare run bundle: %v", err)
	}

	if bundle.RootDir != outDir {
		t.Fatalf("unexpected bundle root: got=%q want=%q", bundle.RootDir, outDir)
	}
	if bundle.RecordingPath != filepath.Join(outDir, "recording.mkv") {
		t.Fatalf("unexpected recording path: %q", bundle.RecordingPath)
	}

	raw, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatalf("read initial manifest: %v", err)
	}
	var manifest RunManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse initial manifest: %v", err)
	}
	if manifest.State != bundleStatePreparing || manifest.Stage != "prepare" {
		t.Fatalf("unexpected initial state: %#v", manifest)
	}
}

func TestFinalizeRunBundleNormalizesSessionDirAndWritesManifest(t *testing.T) {
	tmp := t.TempDir()
	bundle, err := PrepareRunBundle(filepath.Join(tmp, "meeting.run"), false)
	if err != nil {
		t.Fatalf("prepare run bundle: %v", err)
	}

	sessionDir := filepath.Join(bundle.RootDir, "sessions", "recording_abc123")
	streamsDir := filepath.Join(sessionDir, "streams")
	if err := os.MkdirAll(streamsDir, 0o755); err != nil {
		t.Fatalf("mkdir streams: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write session json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "events.ndjson"), []byte(""), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	if err := os.WriteFile(bundle.RecordingPath, []byte("mkv"), 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}

	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: "CassiniRecorder"}); err != nil {
		t.Fatalf("finalize run bundle: %v", err)
	}

	if _, err := os.Stat(filepath.Join(bundle.RootDir, "session", "session.json")); err != nil {
		t.Fatalf("expected normalized session dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundle.RootDir, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("expected old sessions dir to be removed, got err=%v", err)
	}

	raw, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest RunManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Kind != "run" {
		t.Fatalf("unexpected manifest kind: %q", manifest.Kind)
	}
	if manifest.State != bundleStateReady || manifest.Stage != "ready" {
		t.Fatalf("unexpected ready state: %#v", manifest)
	}
	if manifest.Recording.Path != "recording.mkv" {
		t.Fatalf("unexpected recording path: %q", manifest.Recording.Path)
	}
	if manifest.Session == nil || manifest.Session.Path != "session" {
		t.Fatalf("unexpected session path: %#v", manifest.Session)
	}
}
