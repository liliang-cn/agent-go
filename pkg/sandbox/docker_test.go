package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not found; skipping docker sandbox tests")
	}
	// Probe the daemon — LookPath alone doesn't guarantee a running daemon.
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		t.Skip("docker daemon not available; skipping docker sandbox tests")
	}
}

func TestDockerExecAndFileRoundtrip(t *testing.T) {
	skipIfNoDocker(t)

	sb, err := NewDocker(WithImage("alpine:latest"), WithNetwork("none"))
	if err != nil {
		t.Skipf("NewDocker failed (image pull/daemon?): %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Exec roundtrip.
	res, err := sb.Exec(ctx, ExecRequest{Command: "sh", Args: []string{"-c", "echo container-hello; exit 7"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(res.Stdout, "container-hello") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", res.ExitCode)
	}

	// File roundtrip.
	if err := sb.WriteFile(ctx, "sub/file.txt", []byte("docker-content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := sb.ReadFile(ctx, "sub/file.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "docker-content" {
		t.Fatalf("read = %q", data)
	}

	entries, err := sb.List(ctx, "sub")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "file.txt" {
		t.Fatalf("list = %+v", entries)
	}

	hits, err := sb.Grep(ctx, "docker-content", GrepOpts{})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected grep hit, got none")
	}
}

func TestDockerPathJail(t *testing.T) {
	skipIfNoDocker(t)
	sb, err := NewDocker(WithImage("alpine:latest"))
	if err != nil {
		t.Skipf("NewDocker failed: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	if _, err := sb.containerPath("../../etc/passwd"); err == nil {
		t.Fatal("containerPath allowed traversal")
	}
}
