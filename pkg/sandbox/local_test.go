package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestLocal(t *testing.T) *LocalSandbox {
	t.Helper()
	sb, err := NewLocal()
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func TestLocalPathJailRejectsEscapes(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()

	escapes := []string{
		"../etc/passwd",
		"../../secret",
		"a/b/../../../escape",
		"/etc/passwd", // absolute outside workspace -> remapped, must stay inside
	}
	for _, p := range escapes {
		// WriteFile must never create files outside the workspace.
		err := sb.WriteFile(ctx, p, []byte("x"), 0o644)
		if p == "/etc/passwd" {
			// Absolute is remapped under the workspace; should succeed inside.
			if err != nil {
				t.Fatalf("remapped absolute write failed: %v", err)
			}
			// Verify it landed inside the workspace.
			if _, statErr := os.Stat(filepath.Join(sb.Workspace(), "etc", "passwd")); statErr != nil {
				t.Fatalf("remapped file not inside workspace: %v", statErr)
			}
			continue
		}
		if err == nil {
			t.Fatalf("expected escape rejection for %q, got nil", p)
		}
		if !strings.Contains(err.Error(), "escape") {
			t.Fatalf("unexpected error for %q: %v", p, err)
		}
	}

	// Direct helper checks.
	if _, err := sb.resolveInWorkspace("../x"); err == nil {
		t.Fatal("resolveInWorkspace allowed traversal")
	}
	if got, err := sb.resolveInWorkspace("sub/dir"); err != nil || !strings.HasPrefix(got, sb.Workspace()) {
		t.Fatalf("resolveInWorkspace(sub/dir) = %q, %v", got, err)
	}
}

func TestLocalExecCapturesOutput(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()

	res, err := sb.Exec(ctx, ExecRequest{Command: "sh", Args: []string{"-c", "echo hello; echo oops 1>&2; exit 3"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) != "oops" {
		t.Fatalf("stderr = %q", res.Stderr)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestLocalExecStdinAndWorkdir(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()
	if err := sb.Mkdir(ctx, "wd"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	res, err := sb.Exec(ctx, ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "cat; pwd"},
		Stdin:   "piped\n",
		Workdir: "wd",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(res.Stdout, "piped") {
		t.Fatalf("stdin not echoed: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "wd") {
		t.Fatalf("workdir not applied: %q", res.Stdout)
	}
}

func TestLocalExecTimeout(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()

	start := time.Now()
	res, _ := sb.Exec(ctx, ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "sleep 10"},
		Timeout: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not fire, took %v", elapsed)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit after timeout, got 0")
	}
}

func TestLocalFileRoundtrip(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()

	if err := sb.WriteFile(ctx, "dir/a.txt", []byte("content-a"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := sb.ReadFile(ctx, "dir/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "content-a" {
		t.Fatalf("read = %q", data)
	}

	info, err := sb.Stat(ctx, "dir/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "a.txt" || info.Size != int64(len("content-a")) || info.IsDir {
		t.Fatalf("stat = %+v", info)
	}

	entries, err := sb.List(ctx, "dir")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("list = %+v", entries)
	}

	if err := sb.Move(ctx, "dir/a.txt", "dir2/b.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := sb.ReadFile(ctx, "dir/a.txt"); err == nil {
		t.Fatal("source still exists after move")
	}
	data, err = sb.ReadFile(ctx, "dir2/b.txt")
	if err != nil || string(data) != "content-a" {
		t.Fatalf("moved file read = %q, %v", data, err)
	}

	if err := sb.Remove(ctx, "dir2", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := sb.Stat(ctx, "dir2"); err == nil {
		t.Fatal("dir2 still exists after remove")
	}
}

func TestLocalGlobAndGrep(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()

	_ = sb.WriteFile(ctx, "x.go", []byte("package x\nfunc Foo() {}\n"), 0o644)
	_ = sb.WriteFile(ctx, "y.go", []byte("package y\nfunc Bar() {}\n"), 0o644)
	_ = sb.WriteFile(ctx, "z.txt", []byte("not go\nfoo here\n"), 0o644)

	matches, err := sb.Glob(ctx, "*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("glob *.go = %v", matches)
	}

	hits, err := sb.Grep(ctx, "func [A-Z]", GrepOpts{Glob: "*.go"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("grep hits = %+v", hits)
	}

	// Case-insensitive match across all files.
	hits, err = sb.Grep(ctx, "FOO", GrepOpts{IgnoreCase: true})
	if err != nil {
		t.Fatalf("Grep ci: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected >=2 case-insensitive hits, got %+v", hits)
	}
}

func TestLocalSnapshotRestore(t *testing.T) {
	sb := newTestLocal(t)
	ctx := context.Background()

	_ = sb.WriteFile(ctx, "keep/a.txt", []byte("alpha"), 0o644)
	_ = sb.WriteFile(ctx, "keep/nested/b.txt", []byte("beta"), 0o644)

	snap, err := sb.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(snap) })

	// Mutate the workspace.
	_ = sb.Remove(ctx, "keep", true)
	_ = sb.WriteFile(ctx, "extra.txt", []byte("should survive restore (additive)"), 0o644)

	if err := sb.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	a, err := sb.ReadFile(ctx, "keep/a.txt")
	if err != nil || string(a) != "alpha" {
		t.Fatalf("restored a.txt = %q, %v", a, err)
	}
	b, err := sb.ReadFile(ctx, "keep/nested/b.txt")
	if err != nil || string(b) != "beta" {
		t.Fatalf("restored b.txt = %q, %v", b, err)
	}
}

func TestLocalShellSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY shell session test is unix-oriented")
	}
	sb := newTestLocal(t)
	ctx := context.Background()

	sess, err := sb.Shell(ctx, ShellOpts{Command: "sh"})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if sess.ID() == "" {
		t.Fatal("empty session id")
	}

	if err := sess.Send("echo roundtrip-marker"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sess.Read(0), "roundtrip-marker") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(sess.Read(0), "roundtrip-marker") {
		t.Fatalf("session output missing marker: %q", sess.Read(0))
	}

	if err := sess.Stop(true); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Done() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sess.Done() {
		t.Fatal("session not done after Stop")
	}
}

func TestLocalCustomWorkspace(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ws")
	sb, err := NewLocal(WithWorkspace(sub))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	if err := sb.WriteFile(context.Background(), "f.txt", []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A provided workspace must NOT be removed on Close.
	if err := sb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("provided workspace was removed on Close: %v", err)
	}
}
