package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// The system prompt must name the directory the agent's tools actually run in.
// Naming the host process's directory instead sends a coding agent `cd`-ing
// out of its sandbox on the first round.
func TestSystemContextWorkingDirIsTheSandboxWorkspace(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspace")
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(ws))
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	svc := &Service{execSandbox: sb}
	got := svc.buildSystemContext().WorkingDir

	want := sb.Workspace()
	if got != want {
		t.Fatalf("system prompt names %q, but tools run in %q", got, want)
	}
	if strings.Contains(svc.buildSystemContext().FormatForPrompt(), getCwd()) && getCwd() != want {
		t.Fatalf("prompt still leaks the host process directory %q", getCwd())
	}
}

// Without a sandbox the process directory is the honest answer: that is where
// the tools really run.
func TestSystemContextWorkingDirFallsBackToProcessDir(t *testing.T) {
	svc := &Service{}
	if got, want := svc.buildSystemContext().WorkingDir, getCwd(); got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}
