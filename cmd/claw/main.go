// Command claw is an interactive, autonomous command-line agent built on
// AgentGo — a Claude-Code / Manus-style CLI. It gives the model a sandboxed
// workspace, a browser, vision, and a long-horizon autonomy budget, then lets
// you converse with it across turns while it plans and acts with real tools.
//
// Capabilities wired in (all from the framework core):
//
//	sandbox  → fs_* / bash / shell_* tools, jailed to a workspace
//	browser  → browser_* tools (navigate/read/click/type/screenshot/...)
//	vision   → screenshots fed back to vision-capable models
//	autonomy → large round budget + a scratchpad todo list
//	deliverables → the files the agent leaves in its workspace
//
// LLM: uses $LLM_BASE/$LLM_KEY/$LLM_MODEL (or $DASHSCOPE_API_KEY) when set;
// otherwise falls back to the provider configured in agentgo.toml.
//
// Usage:
//
//	claw                                  # interactive REPL
//	claw -p "research X and write report.md"   # one-shot, then exit
//	claw --workspace ./work --docker      # persistent workspace, docker sandbox
//	claw --no-browser                     # disable the browser
//
// In the REPL: type a task and press enter. Slash commands:
//
//	/help   /files   /deliverables   /workspace   /new   /exit
//
// Ctrl-C aborts the current task and returns to the prompt; Ctrl-D (EOF) exits.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/browser"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cCyan   = "\033[36m"
	cGreen  = "\033[1;32m"
	cYellow = "\033[1;33m"
	cRed    = "\033[1;31m"
	cMag    = "\033[35m"
)

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	oneShot := flag.String("p", "", "one-shot task: run it, print the result, then exit")
	sandboxKind := flag.String("sandbox", envOr("AGENT_SANDBOX", "local"), "sandbox backend: local | docker")
	docker := flag.Bool("docker", false, "shorthand for -sandbox docker")
	noBrowser := flag.Bool("no-browser", false, "disable the browser tools")
	headless := flag.Bool("headless", true, "run the browser headless")
	maxRounds := flag.Int("max-rounds", 60, "max tool rounds per task (autonomy budget)")
	workspace := flag.String("workspace", "", "workspace dir (default: a temp dir cleaned on exit)")
	model := flag.String("model", "", "override the model name")
	flag.Parse()

	if *docker {
		*sandboxKind = "docker"
	}

	svc, sb, br, cleanup := build(*sandboxKind, *workspace, *noBrowser, *headless, *maxRounds, *model)
	defer cleanup()

	session := uuid.NewString()

	banner(svc, sb, br, *maxRounds)

	// One-shot mode.
	if strings.TrimSpace(*oneShot) != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		runTurn(ctx, svc, sb, session, *maxRounds, *oneShot)
		printDeliverables(context.Background(), svc)
		return
	}

	repl(svc, sb, session, *maxRounds)
	printDeliverables(context.Background(), svc)
}

// build assembles the agent + its execution capabilities.
func build(sandboxKind, workspace string, noBrowser, headless bool, maxRounds int, model string) (*agent.Service, sandbox.Sandbox, browser.Browser, func()) {
	// --- Sandbox ---
	var sb sandbox.Sandbox
	var err error
	if sandboxKind == "docker" {
		if sb, err = sandbox.NewDocker(); err != nil {
			log.Printf("[WARN] docker sandbox unavailable (%v); falling back to local", err)
		}
	}
	if sb == nil {
		opts := []sandbox.LocalOption{}
		if strings.TrimSpace(workspace) != "" {
			opts = append(opts, sandbox.WithWorkspace(workspace))
		}
		if sb, err = sandbox.NewLocal(opts...); err != nil {
			log.Fatalf("build sandbox: %v", err)
		}
	}

	// --- Browser (optional) ---
	var br browser.Browser
	if !noBrowser {
		if br, err = browser.NewChromedp(browser.WithHeadless(headless)); err != nil {
			log.Printf("[WARN] browser unavailable (%v); running without browser", err)
			br = nil
		}
	}

	// --- Agent ---
	b := agent.New("claw").
		WithPrompt("You are claw, an autonomous command-line agent. You have a sandboxed workspace, " +
			"shell, file tools, and a browser. Plan multi-step work with the scratchpad, gather facts with " +
			"the browser, and produce concrete results as files in your workspace. Always act with tools — " +
			"never just describe what you would do. Keep the user informed with short progress notes.").
		WithSandbox(sb).
		WithVision(true).
		WithDeliverables(true).
		WithAutonomy(agent.AutonomyProfile{MaxRounds: maxRounds, Scratchpad: true})
	if br != nil {
		b = b.WithBrowser(br)
	}

	// LLM: env-configured pool if a key is present, else the agentgo.toml provider.
	if key := envOr("LLM_KEY", os.Getenv("DASHSCOPE_API_KEY")); key != "" {
		m := envOr("LLM_MODEL", "qwen-plus")
		if strings.TrimSpace(model) != "" {
			m = model
		}
		brain, err := pool.NewPool(pool.PoolConfig{
			Enabled: true, Strategy: pool.StrategyRoundRobin,
			Providers: []pool.Provider{{
				Name:    "brain",
				BaseURL: envOr("LLM_BASE", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
				Key:     key, ModelName: m, MaxConcurrency: 5, Capability: 8,
			}},
		})
		if err != nil {
			log.Fatalf("build llm: %v", err)
		}
		b = b.WithLLM(brain)
	}

	svc, err := b.Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}

	cleanup := func() {
		_ = svc.Close()
		if br != nil {
			_ = br.Close()
		}
		_ = sb.Close()
	}
	return svc, sb, br, cleanup
}

func banner(svc *agent.Service, sb sandbox.Sandbox, br browser.Browser, maxRounds int) {
	fmt.Printf("%s%s claw %s autonomous agent CLI\n\n", cCyan, cBold, cReset)
	fmt.Printf("%sworkspace%s %s\n", cBold, cReset, sb.Workspace())
	fmt.Printf("%sbrowser%s   %v   %svision%s %v   %smax-rounds%s %d\n",
		cBold, cReset, br != nil, cBold, cReset, svc.VisionEnabled(), cBold, cReset, maxRounds)
	fmt.Printf("%stype a task, or /help. Ctrl-C aborts a task, Ctrl-D exits.%s\n\n", cDim, cReset)
}

// repl runs the interactive loop.
func repl(svc *agent.Service, sb sandbox.Sandbox, session string, maxRounds int) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for {
		fmt.Printf("%syou ▸%s ", cGreen, cReset)
		if !in.Scan() {
			fmt.Println("\nbye.")
			return
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if slash(line, svc, sb, &session) {
				return // /exit
			}
			continue
		}
		// Per-turn cancellable context: Ctrl-C aborts the task, not the REPL.
		ctx, cancel := context.WithCancel(context.Background())
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		go func() {
			<-sigCh
			fmt.Printf("\n%s⨯ aborting task…%s\n", cYellow, cReset)
			cancel()
		}()
		runTurn(ctx, svc, sb, session, maxRounds, line)
		signal.Stop(sigCh)
		cancel()
	}
}

// slash handles /commands. Returns true if the REPL should exit.
func slash(line string, svc *agent.Service, sb sandbox.Sandbox, session *string) bool {
	cmd := strings.Fields(line)[0]
	switch cmd {
	case "/exit", "/quit", "/q":
		fmt.Println("bye.")
		return true
	case "/help", "/h":
		fmt.Printf("%scommands:%s /files  /deliverables  /workspace  /new  /exit\n", cBold, cReset)
	case "/workspace", "/ws":
		fmt.Println(sb.Workspace())
	case "/files":
		infos, err := sb.List(context.Background(), ".")
		if err != nil {
			fmt.Printf("%s%v%s\n", cRed, err, cReset)
			break
		}
		for _, f := range infos {
			kind := "f"
			if f.IsDir {
				kind = "d"
			}
			fmt.Printf("  %s %8d  %s\n", kind, f.Size, f.Path)
		}
	case "/deliverables":
		printDeliverables(context.Background(), svc)
	case "/new":
		*session = uuid.NewString()
		fmt.Printf("%snew session started (history cleared)%s\n", cDim, cReset)
	default:
		fmt.Printf("%sunknown command %q — try /help%s\n", cDim, cmd, cReset)
	}
	return false
}

// runTurn streams one task to completion with a live trace.
func runTurn(ctx context.Context, svc *agent.Service, sb sandbox.Sandbox, session string, maxRounds int, task string) {
	events, err := svc.RunStreamWithOptions(ctx, task,
		agent.WithSessionID(session), agent.WithMaxTurns(maxRounds))
	if err != nil {
		fmt.Printf("%srun: %v%s\n", cRed, err, cReset)
		return
	}
	var lastPartial bool
	for ev := range events {
		switch ev.Type {
		case agent.EventTypePartial:
			fmt.Print(ev.Content)
			lastPartial = true
		case agent.EventTypeToolCall:
			if lastPartial {
				fmt.Println()
				lastPartial = false
			}
			fmt.Printf("%s▶ %-18s%s %s\n", cCyan, ev.ToolName, cReset, briefArgs(ev.ToolArgs))
		case agent.EventTypeToolResult:
			fmt.Printf("  %s%s%s\n", cDim, briefResult(ev.ToolResult), cReset)
		case agent.EventTypeHandoff:
			fmt.Printf("%s⇄ handoff → %s%s\n", cMag, ev.AgentName, cReset)
		case agent.EventTypeCompactBoundary:
			fmt.Printf("%s… history compacted%s\n", cYellow, cReset)
		case agent.EventTypeComplete:
			if lastPartial {
				fmt.Println()
			}
			fmt.Printf("\n%sclaw ▸%s %s\n\n", cGreen, cReset, strings.TrimSpace(ev.Content))
		case agent.EventTypeBlocked:
			fmt.Printf("\n%s⨯ blocked:%s %s\n\n", cYellow, cReset, strings.TrimSpace(ev.Content))
		case agent.EventTypeError:
			fmt.Printf("\n%s⨯ error:%s %s\n\n", cRed, cReset, strings.TrimSpace(ev.Content))
		}
	}
}

func printDeliverables(ctx context.Context, svc *agent.Service) {
	ds, err := svc.Deliverables(ctx)
	if err != nil || len(ds) == 0 {
		return
	}
	fmt.Printf("%s--- deliverables (%d) ---%s\n", cBold, len(ds), cReset)
	for _, d := range ds {
		fmt.Printf("  %-22s %8d bytes  [%s]\n", d.Path, d.Size, d.Type)
	}
}

func briefArgs(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		s := strings.ReplaceAll(fmt.Sprintf("%v", v), "\n", " ")
		if len(s) > 60 {
			s = s[:60] + "…"
		}
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
		s := strings.ReplaceAll(fmt.Sprintf("%v", res), "\n", " ")
		if len(s) > 100 {
			s = s[:100] + "…"
		}
		return s
	}
	if okv, _ := m["ok"].(bool); !okv {
		return "✗ " + fmt.Sprintf("%v", m["error"])
	}
	s := strings.ReplaceAll(fmt.Sprintf("✓ %v", m["data"]), "\n", " ")
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	return s
}
