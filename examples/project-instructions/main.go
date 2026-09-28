// Package main shows WithProjectInstructions: AGENTS.md (and any other
// instruction file names you choose) found from the agent's workspace upward
// and placed in the static part of the system prompt.
//
// The example builds a small repository in a temp directory — a root
// AGENTS.md, a service-level AGENTS.md and a CLAUDE.md — points a sandbox at
// the service directory, and prints the system prompt with Service.Preview.
// No model is called.
//
// Rules worth knowing:
//   - Discovery starts at the sandbox workspace when there is one (never the
//     host process directory), and stops at the repository root (.git), at
//     ProjectInstructions.StopDir, or at the filesystem root.
//   - Every file found is used, nearest first; the prompt tells the model a
//     nearer file wins where two disagree. The size cap is spent nearest first
//     too, and a cut is stated in the prompt.
//   - Files are read once per run. Unchanged files give byte-identical prompts
//     across rounds and runs, so a provider's prefix cache holds.
//
// Usage:
//
//	go run ./examples/project-instructions
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo, err := os.MkdirTemp("", "project-instructions-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(repo)

	must(os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	must(os.MkdirAll(filepath.Join(repo, "services", "billing"), 0o755))
	write(filepath.Join(repo, "AGENTS.md"), "- Run `go test ./...` before calling a task done.\n- Never commit generated files.")
	write(filepath.Join(repo, "services", "billing", "AGENTS.md"), "- Amounts are integer cents; never use float64 for money.")
	write(filepath.Join(repo, "services", "billing", "CLAUDE.md"), "- Keep handlers under 50 lines.")

	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(filepath.Join(repo, "services", "billing")))
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Close()

	svc, err := agent.New("project-instructions-demo").
		WithSandbox(sb).
		WithProjectInstructions(agent.ProjectInstructions{
			Files:    []string{"AGENTS.md", "CLAUDE.md"},
			MaxBytes: 16 * 1024,
		}).
		Build()
	if err != nil {
		log.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	p, err := svc.Preview(ctx, "Add a refund endpoint.")
	if err != nil {
		log.Fatalf("preview failed: %v", err)
	}

	start := strings.Index(p.SystemPrompt, "## Project instructions")
	if start < 0 {
		log.Fatal("no project instructions in the system prompt")
	}
	section := p.SystemPrompt[start:]
	if end := strings.Index(section, "\n\n"+agent.SystemPromptDynamicBoundary); end >= 0 {
		section = section[:end]
	}
	fmt.Println(section)
}

func write(path, content string) {
	must(os.WriteFile(path, []byte(content), 0o644))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
