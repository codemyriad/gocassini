package cassini

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
