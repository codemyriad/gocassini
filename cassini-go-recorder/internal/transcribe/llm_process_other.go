//go:build !linux

package transcribe

import "os/exec"

func setSummaryProcessAttributes(cmd *exec.Cmd) {}
