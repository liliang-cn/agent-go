//go:build unix

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func readChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return n
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child pid never written to %s", path)
	return 0
}

func gone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("pid %d still alive %s after the command's context ended", pid, within)
}

// A command whose context ends takes its children with it, including ones that
// ignore SIGTERM, which are SIGKILLed after the grace.
func TestLocalExecCancelKillsWholeGroup(t *testing.T) {
	old := execKillGrace
	execKillGrace = 300 * time.Millisecond
	defer func() { execKillGrace = old }()

	for name, script := range map[string]string{
		"term":       `sleep 60 & echo $! > "$PIDFILE"; wait`,
		"ignoreTerm": `trap '' TERM; sh -c 'trap "" TERM; sleep 60' & echo $! > "$PIDFILE"; wait`,
	} {
		t.Run(name, func(t *testing.T) {
			sb := newTestLocal(t)
			pidfile := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = sb.Exec(ctx, ExecRequest{Command: "sh", Args: []string{"-c", script}, Env: map[string]string{"PIDFILE": pidfile}})
			}()
			child := readChildPID(t, pidfile)
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Exec did not return after cancel")
			}
			waitGone(t, child, 2*time.Second)
		})
	}
}
