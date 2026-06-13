//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

// configureProcAttr puts the command in its own process group so the whole tree
// (including children it spawns) can be signalled/killed together on timeout.
func configureProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the command's process group. Safe to call
// even if the process has already exited.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Negative pid targets the whole process group (Setpgid above makes pgid==pid).
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
