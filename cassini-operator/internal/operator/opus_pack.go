package operator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// packAttemptMeetingToOpus packs one attempt's `.meeting` bundle into that
// attempt's portable `.opus` — the seal (D-583).
//
// This used to be packCanonicalMeetingToOpus: it packed current/<job>.meeting
// into current/<job>.opus, from a detached goroutine, additively, and a failure
// was logged and forgotten because `.meeting` was still the publish input. Two
// things changed and both matter:
//
//   - The input is the *attempt's* bundle and the output is the *attempt's*
//     path, so two reruns of one job cannot write the same file. The canonical
//     current/<job>.opus is a promotion of this artifact, not an independent
//     pack of it (promoteOpusFile).
//   - Failure is fatal. Nothing downstream falls back to `.meeting` any more, so
//     a pack that did not produce a verified file must fail the job rather than
//     let it publish something else.
//
// `cassini pack` verifies its own output before renaming it into place: it
// re-decodes the packed file and compares the PCM SHA-256 against the manifest
// it embedded. A zero exit therefore means "packed and integrity-checked", and
// the operator's own post-conditions are the two things pack cannot tell it —
// that the file is there, and that it is not empty.
func packAttemptMeetingToOpus(ctx context.Context, cassiniBin, workRoot, jobID string, attemptNumber int, title string, logSink io.Writer) (string, error) {
	meetingPath := attemptMeetingPath(workRoot, jobID, attemptNumber)
	if _, err := os.Stat(meetingPath); err != nil {
		return "", fmt.Errorf("stat attempt meeting bundle: %w", err)
	}
	opusPath := attemptOpusPath(workRoot, jobID, attemptNumber)
	if err := os.MkdirAll(filepath.Dir(opusPath), 0o755); err != nil {
		return "", fmt.Errorf("create runs dir for opus: %w", err)
	}

	args := []string{"pack", meetingPath, "--out", opusPath}
	// A Talk recording's room name (resolved at start, talk_room_name.go)
	// becomes the embedded meeting title; without one, pack falls back to the
	// bundle name and the viewer to "Untitled meeting" (D-462).
	if strings.TrimSpace(title) != "" {
		args = append(args, "--title", strings.TrimSpace(title))
	}
	cmd := exec.CommandContext(ctx, cassiniBin, args...)
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
	info, err := os.Stat(opusPath)
	if err != nil {
		return "", fmt.Errorf("pack output missing: %w", err)
	}
	if info.Size() == 0 {
		// A zero-byte `.opus` would sail through every later existence check and
		// fail only in the viewer. Catch it where it is still a seal failure.
		return "", fmt.Errorf("pack output is empty: %s", opusPath)
	}
	return opusPath, nil
}
