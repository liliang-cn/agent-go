// eval-bench runs a fixed agentic task through agent-go and prints an eval-go
// Sample (JSON array) to stdout, for cross-framework benchmarking with eval-go.
//
//	DASHSCOPE_KEY=sk-... GOWORK=off go run ./examples/eval-bench > agentgo.json
//
// Optional TASK_SEED is a JSON object {relpath: content} of files to pre-create
// in the workspace before the run (a small file "database").
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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

// env reads the eval-go ExecTarget variable (EVAL_*) first, then the legacy
// TASK_* one, then the default — so this runner works both when driven by
// eval-go's Bench/ExecTarget and by the standalone Python drivers.
func env(evalKey, taskKey, def string) string {
	if v := os.Getenv(evalKey); v != "" {
		return v
	}
	if v := os.Getenv(taskKey); v != "" {
		return v
	}
	return def
}

func seedWorkspace(ws string) {
	raw := env("EVAL_FILES", "TASK_SEED", "")
	if strings.TrimSpace(raw) == "" {
		return
	}
	var files map[string]string
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		fmt.Fprintln(os.Stderr, "seed parse:", err)
		return
	}
	for p, c := range files {
		full := filepath.Join(ws, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
}

// finalWorkspace returns a readable dump of every file left in the workspace,
// so the eval-go judge can grade against actual end state, not just the answer.
func finalWorkspace(ws string) string {
	var b strings.Builder
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		data, _ := os.ReadFile(p)
		if len(data) > 4000 {
			data = data[:4000]
		}
		fmt.Fprintf(&b, "=== %s ===\n%s\n", rel, string(data))
		return nil
	})
	return b.String()
}

func main() {
	ctx := context.Background()
	ws, _ := os.MkdirTemp("", "bench-ag-")
	seedWorkspace(ws)
	var calls []map[string]any

	taskID := env("EVAL_NAME", "TASK_ID", "baseline")
	task := env("EVAL_INPUT", "TASK_PROMPT", "")
	rubric := env("EVAL_RUBRIC", "TASK_RUBRIC", "")
	var exp []string
	for _, t := range strings.Split(env("EVAL_EXPECTED_TOOLS", "TASK_EXP", ""), ",") {
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
			full := filepath.Join(ws, a.Path)
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
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

	res, err := svc.Run(ctx, task, agent.WithMaxTurns(40))
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
		"context":        []string{"FINAL WORKSPACE FILES:\n" + finalWorkspace(ws)},
		"expected_tools": exp,
		"tool_calls":     calls,
		"trajectory":     traj,
		"rubric":         rubric,
		"meta":           map[string]string{"framework": "agent-go", "lang": "go", "task": taskID},
	}
	fmt.Fprintf(os.Stderr, "ANSWER: %.200s\nTOOLS(%d): %v\n", output, len(calls), res.ToolsUsed)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode([]any{sample})
}
