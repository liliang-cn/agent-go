package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// A sandboxed agent writes every file into its workspace. The lint that
// enforces file deliverables stats the path against the *process's* working
// directory, so the file it demands is the one place it never looks — and the
// run is blocked no matter how many times the agent writes it.
//
// Caught while watching a live run get blocked on AGENT_NOTES.md.
func TestFileDeliverableIsFoundInTheSandboxWorkspace(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()
	if err := sb.WriteFile(context.Background(), "REPORT.md", []byte("the work"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc, err := New("lint-sandbox-artifact").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	lint := FileTaskMustWrite()
	ctx := LintContext{
		Goal:         "write REPORT.md",
		ToolCalls:    []string{"fs_write"},
		Deliverables: []DeliverableRequirement{{Kind: "file", Path: "REPORT.md"}},
		Workspace:    svc.workspaceRoot(),
	}
	ok, reason := lint.Check("Done.", ctx)
	if !ok {
		t.Fatalf("the file is in the workspace and the lint says: %s", reason)
	}
}

// A file that was never written is still missing, workspace or not.
func TestMissingFileDeliverableIsStillRejected(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()

	svc, err := New("lint-sandbox-missing").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	lint := FileTaskMustWrite()
	ok, reason := lint.Check("Done.", LintContext{
		Goal:         "write REPORT.md",
		ToolCalls:    []string{"fs_write"},
		Deliverables: []DeliverableRequirement{{Kind: "file", Path: "REPORT.md"}},
		Workspace:    svc.workspaceRoot(),
	})
	if ok {
		t.Fatal("a file nobody wrote must still be reported missing")
	}
	if !strings.Contains(reason, "REPORT.md") {
		t.Errorf("the reason should name the file: %q", reason)
	}
}

// Without a sandbox nothing changes: the path is resolved as before.
func TestFileDeliverableWithoutASandboxIsUnchanged(t *testing.T) {
	t.Parallel()
	lint := FileTaskMustWrite()
	ok, _ := lint.Check("Done.", LintContext{
		Goal:         "write /definitely/not/here.md",
		ToolCalls:    []string{"fs_write"},
		Deliverables: []DeliverableRequirement{{Kind: "file", Path: "/definitely/not/here.md"}},
	})
	if ok {
		t.Error("a missing absolute path is still missing")
	}
}

// The same blindness lived in a second lint, and fixing only the first one
// moved the block rather than removing it: a live run went from
// "BLOCKED by file_task_must_write" to "BLOCKED by task_delivery_contract",
// with the file sitting on disk both times. Every lint that checks for a
// produced artifact has to resolve it the same way.
func TestEveryFileCheckingLintSeesTheWorkspace(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()
	if err := sb.WriteFile(context.Background(), "REPORT.md", []byte("the work"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc, err := New("lint-sandbox-all").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	ctx := LintContext{
		Goal:           "write REPORT.md",
		ToolCalls:      []string{"fs_write"},
		AvailableTools: []string{"fs_write"},
		Deliverables:   []DeliverableRequirement{{Kind: "file", Path: "REPORT.md", SatisfiedBy: "fs_write"}},
		Workspace:      svc.workspaceRoot(),
	}
	for _, lint := range []OutputLint{FileTaskMustWrite(), TaskDeliveryContract()} {
		if ok, reason := lint.Check("Done.", ctx); !ok {
			t.Errorf("%s cannot see a file that is in the workspace: %s", lint.Name(), reason)
		}
	}
}
