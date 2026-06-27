// eval-bench runs a fixed agentic task through agent-go and prints an eval-go
// Sample (JSON array) to stdout, for cross-framework benchmarking with eval-go.
//
//	DASHSCOPE_KEY=sk-... GOWORK=off go run ./examples/eval-bench > agentgo.json
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
)

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type readArgs struct {
	Path string `json:"path"`
}
type listArgs struct {
	Path string `json:"path"`
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()
	ws, _ := os.MkdirTemp("", "bench-ag-")
	var calls []map[string]any

	taskID := getenv("TASK_ID", "baseline")
	task := os.Getenv("TASK_PROMPT")
	rubric := os.Getenv("TASK_RUBRIC")
	var exp []string
	for _, t := range strings.Split(os.Getenv("TASK_EXP"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			exp = append(exp, t)
		}
	}

	// Construct the qwen provider directly (LLMProvider embeds domain.Generator),
	// bypassing the DB-backed pool so the runner is self-contained.
	llm, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		BaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:   os.Getenv("DASHSCOPE_KEY"),
		LLMModel: "qwen3.7-plus",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "provider:", err)
		os.Exit(1)
	}

	write := agent.NewTool("write_file", "Write content to a file in the workspace (relative path).",
		func(ctx context.Context, a *writeArgs) (any, error) {
			calls = append(calls, map[string]any{"name": "write_file", "args": map[string]any{"path": a.Path, "content": a.Content}})
			if err := os.WriteFile(filepath.Join(ws, a.Path), []byte(a.Content), 0o644); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "bytes": len(a.Content)}, nil
		})
	read := agent.NewTool("read_file", "Read a file from the workspace (relative path).",
		func(ctx context.Context, a *readArgs) (any, error) {
			calls = append(calls, map[string]any{"name": "read_file", "args": map[string]any{"path": a.Path}})
			b, err := os.ReadFile(filepath.Join(ws, a.Path))
			if err != nil {
				return nil, err
			}
			return map[string]any{"content": string(b)}, nil
		})
	list := agent.NewTool("list_dir", "List entries in the workspace (path empty = root).",
		func(_ context.Context, a *listArgs) (any, error) {
			calls = append(calls, map[string]any{"name": "list_dir", "args": map[string]any{"path": a.Path}})
			entries, err := os.ReadDir(filepath.Join(ws, a.Path))
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return map[string]any{"entries": names}, nil
		})

	svc, err := agent.New("bench-agent").
		WithLLM(llm).
		// PTC (the JS tool-orchestration sandbox) is now OFF by default; tools
		// are called directly. Opt in with WithPTC() if you want the sandbox.
		WithPrompt("You are a precise file-working agent. Use the write_file, read_file and list_dir tools to do the work; do not invent results.").
		WithTool(write).
		WithTool(read).
		WithTool(list).
		Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	defer svc.Close()

	res, err := svc.Run(ctx, task)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}

	output := fmt.Sprintf("%v", res.FinalResult)
	traj := make([]string, 0, len(calls))
	for _, c := range calls {
		traj = append(traj, "call "+c["name"].(string))
	}

	sample := map[string]any{
		"name":           taskID + " [agent-go/go]",
		"input":          task,
		"output":         output,
		"expected_tools": exp,
		"tool_calls":     calls,
		"trajectory":     traj,
		"rubric":         rubric,
		"meta":           map[string]string{"framework": "agent-go", "lang": "go", "task": taskID},
	}
	fmt.Fprintf(os.Stderr, "ANSWER: %.200s\nTOOLS: %v\n", output, res.ToolsUsed)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode([]any{sample})
}
