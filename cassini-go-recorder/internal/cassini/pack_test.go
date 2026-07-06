package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setBundleManifestTitle stamps a title into a fixture bundle's cassini.json,
// mirroring what the operator does after promotion (SetMeetingBundleTitle).
func setBundleManifestTitle(t *testing.T, bundleDir, title string) {
	t.Helper()
	manifestPath := filepath.Join(bundleDir, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse fixture manifest: %v", err)
	}
	manifest["title"] = title
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode fixture manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, updated, 0o644); err != nil {
		t.Fatalf("write fixture manifest: %v", err)
	}
}

// readOpusTitleTag returns the TITLE tag of a packed .opus file. Ogg tags can
// surface on the format or the stream depending on the muxer, so both are
// queried.
func readOpusTitleTag(t *testing.T, path string) string {
	t.Helper()
	output, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format_tags=title:stream_tags=title",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe title tag: %v: %s", err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestPackPrefersManifestTitleOverFileName(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	setBundleManifestTitle(t, bundleDir, "Daily Meeting")

	outPath := filepath.Join(tmp, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got := readOpusTitleTag(t, outPath); got != "Daily Meeting" {
		t.Errorf("packed TITLE = %q, want manifest title %q", got, "Daily Meeting")
	}
}

func TestPackTitleFlagOverridesManifestTitle(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-a.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	setBundleManifestTitle(t, bundleDir, "Manifest Title")

	outPath := filepath.Join(tmp, "meeting-a.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath, "--title", "Flag Title"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got := readOpusTitleTag(t, outPath); got != "Flag Title" {
		t.Errorf("packed TITLE = %q, want flag title %q", got, "Flag Title")
	}
}

func TestPackHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini pack ") {
		t.Fatalf("expected pack usage in stderr, got %q", stderr.String())
	}
}

func TestPackListedInRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("root usage exit %d", code)
	}
	if !strings.Contains(stdout.String(), "pack") {
		t.Fatalf("expected pack listed in root usage, got %q", stdout.String())
	}
}

func TestPackRequiresOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", "./some.meeting"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--out is required") {
		t.Fatalf("expected --out required error, got %q", stderr.String())
	}
}

func TestPackRejectsNonOpusOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", "./some.meeting", "--out", "./out.meeting"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be a .opus file") {
		t.Fatalf("expected non-opus output error, got %q", stderr.String())
	}
}

func TestPackRejectsNonMeetingInput(t *testing.T) {
	tmp := t.TempDir()
	notMeeting := filepath.Join(tmp, "not-a-meeting")
	if err := os.MkdirAll(notMeeting, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", notMeeting, "--out", filepath.Join(tmp, "out.opus")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a meeting bundle") {
		t.Fatalf("expected non-meeting input error, got %q", stderr.String())
	}
}

func TestPackRejectsPartialMeetingBundle(t *testing.T) {
	tmp := t.TempDir()
	meeting := filepath.Join(tmp, "demo.meeting")
	if err := os.MkdirAll(meeting, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A meeting manifest that is present but not in the ready state must be
	// refused so we never pack a half-built bundle into a "durable" .opus.
	manifest := `{"kind":"meeting","version":"cassini.meeting.v1","state":"preparing","stage":"build"}`
	if err := os.WriteFile(filepath.Join(meeting, "cassini.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", meeting, "--out", filepath.Join(tmp, "out.opus")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not ready") {
		t.Fatalf("expected not-ready error, got %q", stderr.String())
	}
}
