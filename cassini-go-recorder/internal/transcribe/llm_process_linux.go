package transcribe

import (
	"os/exec"
	"syscall"
)

func setSummaryProcessAttributes(cmd *exec.Cmd) {
	// An operator cancellation kills the recorder process group. The death
	// signal also prevents orphan GPU residency if the recorder is killed alone.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
