// Command autonomous-cli is a CLI front-end that drives an AgentGo autonomous
// agent through a complex, multi-step task and streams every step live so you
// can watch it work: planning (scratchpad), browsing (chromedp), file I/O
// (sandbox), and the deliverables it leaves behind.
//
// It wires the framework's optional execution capabilities:
//
//	WithSandbox  → fs_* / bash / shell_* tools, jailed to a temp workspace
//	WithBrowser  → browser_* tools (navigate/read/screenshot/...)
//	WithVision   → screenshots are fed back to vision-capable models
//	WithAutonomy → larger round budget + a scratchpad todo list
//	WithDeliverables → list_deliverables scans the workspace
//
// Usage:
//
//	LLM_BASE=https://sub.superleo.app/v1 LLM_KEY=sk-... LLM_MODEL=gpt-5.5 \
//	    go run ./examples/autonomous-cli
//
//	# your own task + flags
//	go run ./examples/autonomous-cli -max-rounds 60 -sandbox local "build X and write a report"
//	go run ./examples/autonomous-cli -no-browser "summarize these notes into report.md"
//	AGENT_SANDBOX=docker go run ./examples/autonomous-cli -sandbox docker "..."
package main

import (
	"context"
	"flag"
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

const defaultTask = `Research the Go programming language using the browser, then produce a report in your workspace.
Steps:
1. Plan the work in the scratchpad.
2. Use the browser to open https://go.dev and https://go.dev/doc/ and read the key information.
3. Write a structured report to report.md with three sections: "## Overview", "## Key Features", "## Use Cases".
4. Write sources.md listing every URL you actually visited.
5. Re-read report.md to verify it was written, then finish.
Do the real work with tools — never just describe what you would do.`

func main() {
	sandboxKind := flag.String("sandbox", envOr("AGENT_SANDBOX", "local"), "sandbox backend: local | docker")
	noBrowser := flag.Bool("no-browser", false, "disable the browser tools")
	headless := flag.Bool("headless", true, "run the browser headless")
	maxRounds := flag.Int("max-rounds", 60, "max tool rounds (autonomy budget)")
	flag.Parse()

	// --- Brain ---
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

	// --- Sandbox ---
	var sb sandbox.Sandbox
	if *sandboxKind == "docker" {
		if sb, err = sandbox.NewDocker(); err != nil {
			log.Printf("[WARN] docker sandbox unavailable (%v); falling back to local", err)
		}
	}
	if sb == nil {
		if sb, err = sandbox.NewLocal(); err != nil {
			log.Fatalf("build sandbox: %v", err)
		}
	}
	defer sb.Close()

	// --- Browser (optional) ---
	var br browser.Browser
	if !*noBrowser {
		if br, err = browser.NewChromedp(browser.WithHeadless(*headless)); err != nil {
			log.Printf("[WARN] browser unavailable (%v); running without browser", err)
			br = nil
		}
	}
	if br != nil {
		defer br.Close()
	}

	// --- Build the agent ---
	b := agent.New("autonomous-cli").
		WithLLM(brain).
		WithPrompt("You are an autonomous agent with a sandboxed workspace and a browser. " +
			"Plan with the scratchpad, browse for facts, and write your results to files in the " +
			"workspace. Always act with tools — never just describe what you would do.").
		WithSandbox(sb).
		WithVision(true).
		WithDeliverables(true).
		WithAutonomy(agent.AutonomyProfile{MaxRounds: *maxRounds, Scratchpad: true})
	if br != nil {
		b = b.WithBrowser(br)
	}
	svc, err := b.Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	task := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if task == "" {
		task = defaultTask
	}

	fmt.Printf("\033[1m=== autonomous-cli ===\033[0m\n")
	fmt.Printf("brain     : %s @ %s\n", model, llmBase)
	fmt.Printf("sandbox   : %s  (%s)\n", *sandboxKind, sb.Workspace())
	fmt.Printf("browser   : %v\n", br != nil)
	fmt.Printf("maxRounds : %d\n", *maxRounds)
	fmt.Printf("task      : %s\n\n", firstLine(task))

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	events, err := svc.RunStreamWithOptions(ctx, task, agent.WithMaxTurns(*maxRounds))
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	// --- Live trace ---
	round := 0
	var lastPartial bool
	for ev := range events {
		switch ev.Type {
		case agent.EventTypeThinking:
			// keep it quiet; thinking is noisy
		case agent.EventTypePartial:
			fmt.Print(ev.Content)
			lastPartial = true
		case agent.EventTypeToolCall:
			if lastPartial {
				fmt.Println()
				lastPartial = false
			}
			round++
			fmt.Printf("\033[36m▶ %-18s\033[0m %s\n", ev.ToolName, briefArgs(ev.ToolArgs))
		case agent.EventTypeToolResult:
			fmt.Printf("  \033[2m%s\033[0m\n", briefResult(ev.ToolResult))
		case agent.EventTypeHandoff:
			fmt.Printf("\033[35m⇄ handoff → %s\033[0m\n", ev.AgentName)
		case agent.EventTypeCompactBoundary:
			fmt.Printf("\033[33m… history compacted\033[0m\n")
		case agent.EventTypeComplete:
			if lastPartial {
				fmt.Println()
			}
			fmt.Printf("\n\033[1;32m=== done ===\033[0m\n%s\n", strings.TrimSpace(ev.Content))
		case agent.EventTypeBlocked:
			fmt.Printf("\n\033[1;33m=== blocked ===\033[0m\n%s\n", strings.TrimSpace(ev.Content))
		case agent.EventTypeError:
			fmt.Printf("\n\033[1;31m=== error ===\033[0m\n%s\n", strings.TrimSpace(ev.Content))
		}
	}

	// --- Deliverables + dump the report ---
	deliverables, _ := svc.Deliverables(ctx)
	fmt.Printf("\n\033[1m--- deliverables (%d) ---\033[0m\n", len(deliverables))
	for _, d := range deliverables {
		fmt.Printf("  %-20s %6d bytes  [%s]\n", d.Path, d.Size, d.Type)
	}
	for _, d := range deliverables {
		if strings.HasSuffix(d.Path, "report.md") {
			if data, err := sb.ReadFile(ctx, d.Path); err == nil {
				fmt.Printf("\n\033[1m--- %s ---\033[0m\n%s\n", d.Path, string(data))
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func briefArgs(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		s = strings.ReplaceAll(s, "\n", " ")
		parts = append(parts, k+"="+s)
	}
	out := strings.Join(parts, " ")
	if len(out) > 110 {
		out = out[:110] + "…"
	}
	return out
}

func briefResult(res interface{}) string {
	m, ok := res.(map[string]interface{})
	if !ok {
		s := fmt.Sprintf("%v", res)
		if len(s) > 100 {
			s = s[:100] + "…"
		}
		return strings.ReplaceAll(s, "\n", " ")
	}
	if ok, _ := m["ok"].(bool); !ok {
		return "✗ " + fmt.Sprintf("%v", m["error"])
	}
	s := fmt.Sprintf("✓ %v", m["data"])
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
