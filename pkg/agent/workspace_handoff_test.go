package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// On a coding task the files are most of the state, and a segment that starts
// a fresh session has no other way to know they exist. Without this it either
// spends rounds rediscovering its own output or assumes an empty workspace and
// writes it all again.
func TestSegmentIsToldWhatIsAlreadyInTheWorkspace(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()

	ctx := context.Background()
	if err := sb.WriteFile(ctx, "kernel/boot.S", []byte(".global _start\n_start:\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := sb.WriteFile(ctx, "Makefile", []byte("all:\n\techo hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	llm := &promptCapturingLLM{}
	svc, err := New("workspace-handoff").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(ctx, "Carry on with the kernel.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	prompts := llm.systemPrompts()
	if len(prompts) == 0 {
		t.Fatal("the model was never called")
	}
	for _, want := range []string{"Files already in the workspace", "kernel/boot.S", "Makefile"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("the system prompt never mentioned %q", want)
		}
	}
}

// No sandbox, or an empty one, adds nothing — an empty inventory is prompt the
// model reads on every turn for no reason.
func TestNoWorkspaceMeansNoInventorySection(t *testing.T) {
	llm := &promptCapturingLLM{}
	svc, err := New("workspace-handoff-none").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Start fresh.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}
	for i, p := range llm.systemPrompts() {
		if strings.Contains(p, "Files already in the workspace") {
			t.Errorf("turn %d carried an inventory with no sandbox to inventory", i)
		}
	}
}

// Generated trees are large, uninteresting, and never what the agent needs
// reminding of.
func TestWorkspaceInventorySkipsGeneratedTrees(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()

	ctx := context.Background()
	for _, p := range []string{
		"src/main.go",
		"node_modules/left-pad/index.js",
		".git/objects/ab/cdef",
		"target/debug/binary",
	} {
		if err := sb.WriteFile(ctx, p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	svc, err := New("workspace-inventory-skip").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&promptCapturingLLM{}).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	summary := svc.workspaceSummaryForRun(ctx)
	if !strings.Contains(summary, "src/main.go") {
		t.Error("the inventory should list real work product")
	}
	for _, skipped := range []string{"left-pad", ".git/objects", "target/debug"} {
		if strings.Contains(summary, skipped) {
			t.Errorf("the inventory should skip %q", skipped)
		}
	}
}

// The inventory sits in a prompt that must stay byte-stable for a whole
// segment, so it is bounded rather than however big the tree happens to be.
func TestWorkspaceInventoryIsBounded(t *testing.T) {
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(t.TempDir()))
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer sb.Close()

	ctx := context.Background()
	for i := 0; i < workspaceHandoffMaxEntries+80; i++ {
		if err := sb.WriteFile(ctx, paddedName(i), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	svc, err := New("workspace-inventory-bounded").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&promptCapturingLLM{}).
		WithSandbox(sb).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	summary := svc.workspaceSummaryForRun(ctx)
	lines := strings.Count(summary, "\n") + 1
	if lines > workspaceHandoffMaxEntries+8 {
		t.Errorf("the inventory ran to %d lines; it must stay bounded", lines)
	}
	if !strings.Contains(summary, "use fs_list and fs_glob for the rest") {
		t.Error("a truncated inventory should say so and point at the tools")
	}
}

// paddedName keeps the generated names a fixed width so the listing sorts the
// way a reader expects.
func paddedName(i int) string {
	return fmt.Sprintf("f%04d.txt", i)
}
