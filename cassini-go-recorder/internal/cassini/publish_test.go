package cassini

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReadyMeetingBundle(t *testing.T, meetingDir string) {
	t.Helper()
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		t.Fatalf("mkdir meeting dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "cassini.json"), []byte(`{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "ready",
  "stage": "ready",
  "source_kind": "mkv",
  "source_path": "/tmp/source.mkv"
}
`), 0o644); err != nil {
		t.Fatalf("write meeting manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "meeting.webm"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("write meeting webm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript.words.v1.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write artifact manifest: %v", err)
	}
}

func writeCorruptBundleManifest(t *testing.T, bundleDir string) {
	t.Helper()
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("mkdir bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "cassini.json"), []byte(`{"kind": "meeting",`), 0o644); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}
}

func TestPublishSkipsCorruptBundleManifests(t *testing.T) {
	tmp := t.TempDir()
	exporter := filepath.Join(tmp, "fake-exporter.sh")
	if err := os.WriteFile(exporter, []byte(`#!/usr/bin/env bash
set -euo pipefail
SOURCE_DIR=""
OUTPUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-dir)
      SOURCE_DIR="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$OUTPUT_DIR"
printf '<html></html>' >"$OUTPUT_DIR/index.html"
ls "$SOURCE_DIR" >"$OUTPUT_DIR/source-listing.txt"
`), 0o755); err != nil {
		t.Fatalf("write fake exporter: %v", err)
	}

	meetingsDir := filepath.Join(tmp, "meetings")
	writeReadyMeetingBundle(t, filepath.Join(meetingsDir, "good.meeting"))
	// One corrupt meeting bundle and one corrupt run bundle (whose manifest is
	// parsed before the Kind filter) must not abort the publish of `good`.
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "broken.meeting"))
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "job.run"))

	t.Setenv("CASSINI_EXPORTER_RUNNER", exporter)
	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingsDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected publish success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	listing, err := os.ReadFile(filepath.Join(outDir, "source-listing.txt"))
	if err != nil {
		t.Fatalf("read staged source listing: %v", err)
	}
	if !strings.Contains(string(listing), "good") {
		t.Fatalf("expected good meeting staged, got listing=%q", string(listing))
	}
	if strings.Contains(string(listing), "broken") || strings.Contains(string(listing), "job") {
		t.Fatalf("expected corrupt bundles excluded from staging, got listing=%q", string(listing))
	}
	for _, name := range []string{"broken.meeting", "job.run"} {
		if !strings.Contains(stdout.String(), "skipped "+filepath.Join(meetingsDir, name)) {
			t.Fatalf("expected skip line for %s in stdout, got %q", name, stdout.String())
		}
	}
}

func TestPublishFailsWhenOnlyBundleIsCorrupt(t *testing.T) {
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "broken.meeting"))

	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingsDir, "--out", outDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected publish failure, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no ready meeting bundles found") {
		t.Fatalf("expected no-ready-bundles error, got stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "broken.meeting") || !strings.Contains(stderr.String(), "parse meeting bundle manifest") {
		t.Fatalf("expected corrupt bundle skip detail with path, got stderr=%q", stderr.String())
	}
}

func TestStagePublishInputSkipsCorruptBundles(t *testing.T) {
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	writeReadyMeetingBundle(t, filepath.Join(meetingsDir, "good.meeting"))
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "broken.meeting"))
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "job.run"))

	stagingRoot, sourceSummary, skipped, err := stagePublishInput(meetingsDir)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)

	if sourceSummary != meetingsDir {
		t.Fatalf("expected source summary %q, got %q", meetingsDir, sourceSummary)
	}
	if _, err := os.Stat(filepath.Join(stagingRoot, "good", "cassini.json")); err != nil {
		t.Fatalf("expected good bundle staged: %v", err)
	}
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the good bundle staged, got %d entries", len(entries))
	}
	if len(skipped) != 2 {
		t.Fatalf("expected two skipped bundles, got %+v", skipped)
	}
	for _, item := range skipped {
		if !strings.Contains(item.Reason, "parse meeting bundle manifest") {
			t.Fatalf("expected parse-failure reason, got %+v", item)
		}
		if !strings.Contains(item.Reason, item.Path) {
			t.Fatalf("expected reason to carry the manifest path, got %+v", item)
		}
	}
}

func TestUpdateRunBundleStatusKeepsManifestOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory is not enforced for root")
	}
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "job.run")
	bundle, err := PrepareRunBundle(runDir, false)
	if err != nil {
		t.Fatalf("PrepareRunBundle() error = %v", err)
	}
	before, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// A read-only bundle directory makes the temp-file write fail before the
	// rename; the previous manifest must survive untouched.
	if err := os.Chmod(runDir, 0o555); err != nil {
		t.Fatalf("chmod run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o755) })

	if err := UpdateRunBundleStatus(bundle, bundleStateFailed, "record", "boom"); err == nil {
		t.Fatalf("expected manifest write failure in read-only dir")
	}
	after, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest after failed write: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("expected manifest unchanged after failed write, before=%q after=%q", string(before), string(after))
	}
}
