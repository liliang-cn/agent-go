// Package main demonstrates a multi-step, multi-agent collaboration where
// AgentGo's Operator agent orchestrates the locally-installed Codex and Claude
// Code CLIs to build, review, fix, and verify a piece of code together.
//
// Collaboration flow (all four steps run through the Operator agent, driven by
// the configured LLM "brain", via a RunPipeline):
//
//  1. codex   — implement IsPalindrome in palindrome.go
//  2. claude  — review the code, write findings to REVIEW.md
//  3. codex   — read REVIEW.md, revise + add table-driven tests
//  4. claude  — run `go test` and report the final verdict
//
// The agents collaborate through a SHARED workspace directory: code and the
// review live on disk, so nothing lossy has to pass through the LLM between
// steps — the pipeline's {input} only carries short status summaries.
//
// Requirements:
//   - `codex` and `claude` CLIs installed and authenticated on PATH.
//   - A capable LLM brain. This example uses Alibaba DashScope (OpenAI-compatible)
//     by default; export DASHSCOPE_API_KEY (and optionally DASHSCOPE_MODEL).
//
// Usage:
//
//	DASHSCOPE_API_KEY=sk-... go run ./examples/operator-collab
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/v2/examples/internal/teamdemo"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
)

func main() {
	if _, err := exec.LookPath("codex"); err != nil {
		log.Fatalf("codex CLI not found on PATH: %v", err)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		log.Fatalf("claude CLI not found on PATH: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	manager, cfg, err := teamdemo.NewManager("operator-collab")
	if err != nil {
		log.Fatalf("setup manager: %v", err)
	}

	// Point the Operator's brain at a capable cloud model. The repo default
	// (local ollama) is too weak for reliable multi-step tool-calling.
	apiKey := os.Getenv("DASHSCOPE_API_KEY")
	if apiKey == "" {
		log.Fatalf("DASHSCOPE_API_KEY is required (OpenAI-compatible Qwen endpoint)")
	}
	model := os.Getenv("DASHSCOPE_MODEL")
	if model == "" {
		model = "qwen-plus"
	}
	brain, err := pool.NewPool(pool.PoolConfig{
		Enabled:  true,
		Strategy: pool.StrategyRoundRobin,
		Providers: []pool.Provider{{
			Name:           "dashscope",
			BaseURL:        "https://dashscope.aliyuncs.com/compatible-mode/v1",
			Key:            apiKey,
			ModelName:      model,
			MaxConcurrency: 5,
			Capability:     8,
		}},
	})
	if err != nil {
		log.Fatalf("build llm pool: %v", err)
	}
	manager.SetConfig(cfg)
	manager.SetLLM(brain)          // inject the brain directly (bypass global pool)
	manager.SetDisableMemory(true) // no embedding dependency for this demo

	// Shared workspace the coding agents collaborate in.
	ws, err := os.MkdirTemp("", "operator-collab-ws-")
	if err != nil {
		log.Fatalf("create workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module collab\n\ngo 1.21\n"), 0644); err != nil {
		log.Fatalf("write go.mod: %v", err)
	}

	fmt.Println("=== Operator multi-agent collaboration (codex + claude) ===")
	fmt.Printf("Brain model : %s @ dashscope\n", model)
	fmt.Printf("Workspace   : %s\n\n", ws)

	codexFlags := `["--skip-git-repo-check","--dangerously-bypass-approvals-and-sandbox"]`
	claudeFlags := `["--dangerously-skip-permissions"]`

	steps := []agent.PipelineStep{
		{
			AgentName: "Operator",
			Prompt: "调用工具 run_coding_agent_once，参数严格如下：" +
				`provider="codex"，workdir="` + ws + `"，extra_args=` + codexFlags + `，` +
				`prompt="在当前目录创建 palindrome.go（package collab），实现 func IsPalindrome(s string) bool：忽略大小写、忽略所有非字母数字字符后判断是否回文。先给一个最简单的版本即可，暂时不用写测试。只创建/修改文件。"。` +
				"工具返回后，用一句中文总结 codex 做了什么，只输出这句话。",
		},
		{
			AgentName: "Operator",
			Prompt: "调用工具 run_coding_agent_once，参数严格如下：" +
				`provider="claude"，workdir="` + ws + `"，extra_args=` + claudeFlags + `，` +
				`prompt="审阅当前目录 palindrome.go 里的 IsPalindrome 实现，找出 bug、未覆盖的边界条件（空串、Unicode、大小写、数字）和改进点；把审阅意见写入当前目录的 REVIEW.md 文件。不要修改 palindrome.go。"。` +
				"工具返回后，用一句中文说明 claude 是否已写出 REVIEW.md，只输出这句话。\n\n上游：{input}",
		},
		{
			AgentName: "Operator",
			Prompt: "调用工具 run_coding_agent_once，参数严格如下：" +
				`provider="codex"，workdir="` + ws + `"，extra_args=` + codexFlags + `，` +
				`prompt="阅读当前目录的 REVIEW.md，根据其中的审阅意见修订 palindrome.go，并创建表驱动测试 palindrome_test.go（package collab，覆盖空串、大小写、含标点/空格、Unicode、非回文等用例），确保在该目录运行 go test ./... 能通过。"。` +
				"工具返回后，用一句中文总结你的修订，只输出这句话。\n\n上游：{input}",
		},
		{
			AgentName: "Operator",
			Prompt: "调用工具 run_coding_agent_once，参数严格如下：" +
				`provider="claude"，workdir="` + ws + `"，extra_args=` + claudeFlags + `，` +
				`prompt="在当前目录运行 go test ./... 。如果全部通过，最后单独输出一行 FINAL: PASS；否则输出一行 FINAL: FAIL 并附一句原因。"。` +
				"把 claude 的最终结论原样作为你的回答返回。\n\n上游：{input}",
		},
	}

	events, err := manager.RunPipeline(ctx, steps, "")
	if err != nil {
		log.Fatalf("run pipeline: %v", err)
	}

	results, err := agent.CollectPipelineResult(events)
	if err != nil {
		log.Printf("pipeline error: %v", err)
	}

	labels := []string{
		"Step 1  codex  — implement",
		"Step 2  claude — review → REVIEW.md",
		"Step 3  codex  — revise + tests",
		"Step 4  claude — go test + verdict",
	}
	for i, r := range results {
		label := fmt.Sprintf("Step %d", i+1)
		if i < len(labels) {
			label = labels[i]
		}
		fmt.Printf("\n----- %s -----\n%s\n", label, r)
	}

	// Independent verification: list the collaboratively-built files and run go test.
	fmt.Printf("\n===== Workspace files (%s) =====\n", ws)
	entries, _ := os.ReadDir(ws)
	for _, e := range entries {
		fmt.Printf("  %s\n", e.Name())
	}

	fmt.Println("\n===== Independent `go test ./...` =====")
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = ws
	out, testErr := cmd.CombinedOutput()
	fmt.Print(string(out))
	if testErr != nil {
		fmt.Printf("go test failed: %v\n", testErr)
		os.Exit(1)
	}
	fmt.Println("collaboration verified ✔")
}
