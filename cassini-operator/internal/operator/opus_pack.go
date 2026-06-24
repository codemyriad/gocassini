package operator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// packCanonicalMeetingToOpus packs a promoted `.meeting` bundle in current/
// into a durable `<jobID>.opus` file stored alongside it (D-428).
//
// Why this exists: today an imported/cloud meeting only survives as a
// `.meeting` directory in current/, which publish re-exports on every run.
// Once `.meeting` stops being a publish input (D-429), the durable unit must
// be the `.opus` file. This step writes that `.opus` at promotion time, BEFORE
// `.meeting` is retired, so no imported meeting is ever lost.
//
// IMPORTANT (deploy-ordering / reversibility): this runs ADDITIVELY. It does
// not remove or replace the `.meeting` bundle, and a failure here must NOT fail
// the build — `.meeting` remains the publish input until D-429. The boolean
// return reports whether the `.opus` was written so callers can log/diagnose;
// the error is informational and intentionally non-fatal at the call site.
//
// The pack itself is atomic at the recorder layer (packMeetingBundle stages a
// temp file and renames into place), so a crash mid-pack leaves the previous
// `.opus` (if any) untouched.
func packCanonicalMeetingToOpus(ctx context.Context, cassiniBin, workRoot, jobID string, logSink io.Writer) (string, error) {
	meetingPath := canonicalMeetingPath(workRoot, jobID)
	if _, err := os.Stat(meetingPath); err != nil {
		return "", fmt.Errorf("stat promoted meeting bundle: %w", err)
	}
	opusPath := canonicalOpusPath(workRoot, jobID)
	if err := os.MkdirAll(filepath.Dir(opusPath), 0o755); err != nil {
		return "", fmt.Errorf("create current dir for opus: %w", err)
	}

	cmd := exec.CommandContext(ctx, cassiniBin, "pack", meetingPath, "--out", opusPath)
	if logSink != nil {
		cmd.Stdout = logSink
		cmd.Stderr = logSink
	}
	cmd.Env = os.Environ()
	// Match build/record: kill the whole process group on ctx cancel so any
	// ffmpeg/ffprobe grandchildren spawned by `cassini pack` don't outlive it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cassini pack: %w", err)
	}
	if _, err := os.Stat(opusPath); err != nil {
		return "", fmt.Errorf("pack output missing: %w", err)
	}
	return opusPath, nil
}
