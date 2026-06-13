// Package main is a runnable example of an "autonomous agent" built on AgentGo:
// an agent with hands and a body. It wires the optional execution capabilities
// added to the framework core:
//
//   - a sandbox (pkg/sandbox) → fs_* / bash / shell_* tools, jailed to a
//     temp workspace (or a Docker container when AGENT_SANDBOX=docker)
//   - a browser (pkg/browser, chromedp) → browser_* tools (navigate/read/
//     click/type/screenshot/...)
//   - vision → screenshots are surfaced to vision-capable models
//   - autonomy → a larger tool-round budget + a scratchpad todo list
//   - deliverables → list_deliverables scans the workspace for produced files
//
// The brain is any OpenAI-compatible endpoint.
//
// Usage:
//
//	DASHSCOPE_API_KEY=sk-...  go run ./examples/autonomous-agent
//
//	# any OpenAI-compatible brain:
//	LLM_BASE=https://host/v1 LLM_KEY=sk-... LLM_MODEL=gpt-5.4 \
//	    go run ./examples/autonomous-agent
//
//	# strong isolation (requires the `docker` CLI + a running daemon):
//	AGENT_SANDBOX=docker  DASHSCOPE_API_KEY=sk-...  go run ./examples/autonomous-agent
//
//	# pass your own task:
//	DASHSCOPE_API_KEY=sk-...  go run ./examples/autonomous-agent "research X and write a report"
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/browser"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	// --- Brain: any OpenAI-compatible endpoint (default DashScope Qwen). ---
	dashBase := "https://dashscope.aliyuncs.com/compatible-mode/v1"
	llmBase := envOr("LLM_BASE", dashBase)
	llmKey := envOr("LLM_KEY", os.Getenv("DASHSCOPE_API_KEY"))
	model := envOr("LLM_MODEL", "qwen-plus")
	if llmKey == "" {
		log.Fatal("need LLM_KEY (or DASHSCOPE_API_KEY)")
	}
	brain, err := pool.NewPool(pool.PoolConfig{
		Enabled:  true,
		Strategy: pool.StrategyRoundRobin,
		Providers: []pool.Provider{{
			Name: "brain", BaseURL: llmBase, Key: llmKey,
			ModelName: model, MaxConcurrency: 5, Capability: 8,
		}},
	})
	if err != nil {
		log.Fatalf("build brain: %v", err)
	}

	// --- Sandbox: Local (zero-dep, temp workspace) or Docker (strong isolation). ---
	var sb sandbox.Sandbox
	if envOr("AGENT_SANDBOX", "local") == "docker" {
		sb, err = sandbox.NewDocker()
		if err != nil {
			log.Printf("[WARN] docker sandbox unavailable (%v); falling back to local", err)
		}
	}
	if sb == nil {
		sb, err = sandbox.NewLocal()
		if err != nil {
			log.Fatalf("build sandbox: %v", err)
		}
	}
	defer sb.Close()
	fmt.Printf("workspace: %s\n", sb.Workspace())

	// --- Browser: chromedp, headless. Optional — if no Chrome is installed the
	// example still runs with just the sandbox. ---
	var br browser.Browser
	if br, err = browser.NewChromedp(browser.WithHeadless(true)); err != nil {
		log.Printf("[WARN] browser unavailable (%v); running without browser tools", err)
		br = nil
	}
	if br != nil {
		defer br.Close()
	}

	// --- Build the agent with all execution capabilities. ---
	b := agent.New("autonomous-agent").
		WithLLM(brain).
		WithPrompt("You are an autonomous agent with a sandboxed workspace and a browser. "+
			"Plan your work with the scratchpad, use bash and fs_* tools to read/write files in your "+
			"workspace, browse the web when you need information, and finish by writing your result to a "+
			"file in the workspace. Always do the work — never just describe what you would do.").
		WithSandbox(sb).
		WithVision(true).
		WithDeliverables(true).
		WithAutonomy(agent.AutonomyProfile{MaxRounds: 40, Scratchpad: true})
	if br != nil {
		b = b.WithBrowser(br)
	}
	svc, err := b.Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	// --- The task. ---
	task := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	if task == "" {
		task = "Create a file plan.md in your workspace that lists three uses of an autonomous agent, " +
			"then create report.md summarizing them in two sentences. Use the scratchpad to track your steps."
	}
	fmt.Printf("task: %s\n\n", task)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	res, err := svc.Run(ctx, task, agent.WithMaxTurns(40))
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	fmt.Printf("\n--- final answer ---\n%v\n", res.FinalResult)
	fmt.Printf("(tool calls: %d, tools: %s)\n", res.ToolCalls, strings.Join(res.ToolsUsed, ", "))

	// --- Deliverables: scan the workspace for what the agent produced. ---
	deliverables, err := svc.Deliverables(ctx)
	if err != nil {
		log.Printf("[WARN] scanning deliverables: %v", err)
	}
	fmt.Printf("\n--- deliverables (%d) ---\n", len(deliverables))
	for _, d := range deliverables {
		fmt.Printf("  %-24s %6d bytes  [%s]\n", d.Path, d.Size, d.Type)
	}
}
