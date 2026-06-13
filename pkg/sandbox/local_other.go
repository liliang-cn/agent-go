//go:build !unix

package sandbox

import "os/exec"

// configureProcAttr is a no-op on non-unix platforms.
func configureProcAttr(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing just the process on non-unix platforms.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
