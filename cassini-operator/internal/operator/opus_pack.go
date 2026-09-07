package operator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
// compares the packed Opus audio essence against the digest in the manifest it
// embedded. A zero exit therefore means "packed and integrity-checked", and
// the operator's own post-conditions are the two things pack cannot tell it —
// that the file is there, and that it is not empty.
// The meeting bundle is passed in rather than re-derived: it is what the build
// recorded and what the seal task carries, so a seal packs the bundle the DB
// says it is packing rather than one that merely shares its naming convention.
func packAttemptMeetingToOpus(ctx context.Context, cassiniBin, meetingPath, opusPath, title, roomToken, roomName, jobID string, attemptNumber int, logSink io.Writer) (string, error) {
	if strings.TrimSpace(meetingPath) == "" {
		return "", fmt.Errorf("no meeting bundle to seal")
	}
	if _, err := os.Stat(meetingPath); err != nil {
		return "", fmt.Errorf("stat attempt meeting bundle: %w", err)
	}
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
	// The room travels as its own fields alongside the title, so a consumer can
	// group by which conversation a meeting came from instead of parsing a
	// display string that may not be a room name at all (D-622). Each half is
	// passed only when known — `cassini pack` records an unknown room as absent
	// rather than guessing one.
	//
	// The TOKEN is what is handed over, and it stops at `cassini pack`: the
	// artifact carries only a one-way derivation of it, because for a public
	// conversation the token is also the link that joins it.
	if strings.TrimSpace(roomToken) != "" {
		args = append(args, "--room-token", strings.TrimSpace(roomToken))
	}
	if strings.TrimSpace(roomName) != "" {
		args = append(args, "--room-name", strings.TrimSpace(roomName))
	}
	// Which job and attempt produced this file (D-640). The job id is already
	// the artifact's published name — the sink writes meetings/<jobID>.opus —
	// so recording it inside the file discloses nothing new; the attempt is not
	// recoverable from anything else, and is what tells a rerun's output apart
	// from the output it replaced.
	if strings.TrimSpace(jobID) != "" {
		args = append(args, "--job-id", strings.TrimSpace(jobID))
	}
	if attemptNumber > 0 {
		args = append(args, "--attempt-number", strconv.Itoa(attemptNumber))
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
