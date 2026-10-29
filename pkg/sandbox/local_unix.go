//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
	"time"
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

// terminateProcessGroup asks the command's process group to stop (SIGTERM), so
// a shell and the programs under it can clean up before reapProcessGroup
// forces the rest.
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// reapProcessGroup waits up to grace for every member of the command's process
// group to be gone, then SIGKILLs whatever is left. It is what makes a
// finished or cancelled command leave nothing behind: the leader exiting does
// not stop the children it started.
func reapProcessGroup(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
