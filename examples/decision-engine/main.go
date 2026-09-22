// A decision engine answers small closed questions in milliseconds, so the
// runtime can skip a language-model call it would otherwise make.
//
//	go run ./examples/decision-engine                 # both halves
//	go run ./examples/decision-engine -ask-only       # no provider needed
//
// Needs a decision engine. The default is a laya-serve on this machine:
//
//	cd laya-serve && ./run.sh
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/decision"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

func main() {
	url := flag.String("engine", decision.LayaDefaultURL, "decision engine address")
	askOnly := flag.Bool("ask-only", false, "just ask the engine; do not run an agent")
	flag.Parse()

	engine := decision.NewLaya(decision.WithLayaURL(*url))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := engine.Ready(ctx); err != nil {
		fmt.Printf("no decision engine at %s: %v\n", *url, err)
		fmt.Println("start one, or point -engine elsewhere. Nothing below works without it.")
		os.Exit(1)
	}
	fmt.Printf("engine %s\n\n", engine.Name())

	askDirectly(ctx, engine)
	if *askOnly {
		return
	}
	fmt.Println()
	runWithGate(ctx, engine)
}

// askDirectly uses the engine on its own. Three question types, one request,
// one forward pass — asking them separately would give up the speed.
func askDirectly(ctx context.Context, engine decision.Engine) {
	questions := map[string]decision.Question{
		"kind":   decision.Choice("What must be produced?", "a file", "an email", "a chat message", "nothing"),
		"forbid": decision.Noul("Does the user tell the assistant not to use tools?"),
		"effort": decision.Score("How much work is this?", "a lookup", "a change", "a project"),
	}

	for _, text := range []string{
		"给张伟发一封邮件，说会议改到周四",
		"What is a mutex?",
		"port the storage layer from SQLite to Postgres",
	} {
		started := time.Now()
		answers, err := engine.Decide(ctx, text, questions)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s  (%v)\n", text, time.Since(started).Round(time.Millisecond))
		for _, name := range []string{"kind", "forbid", "effort"} {
			a := answers[name]
			// Confidence is the engine's own, and it is what a caller gates
			// on. It is not the winning probability, and it is the more
			// conservative of the two.
			fmt.Printf("    %-7s %-14s confidence %.2f\n", name, a.Label, a.Confidence)
		}
	}
}

// gateWatch prints what the runtime asked the engine and what it did about it.
// Without something like it the engine is invisible: a run that skipped a call
// and a run that did not look identical.
type gateWatch struct {
	agent.BaseObserver
}

func (gateWatch) OnDecision(_ context.Context, info agent.DecisionInfo) {
	switch {
	case info.Err != nil:
		fmt.Printf("  [gate] %s unavailable: %v — extracting as usual\n", info.Gate, info.Err)
	case info.Skipped:
		fmt.Printf("  [gate] %s: nothing asked for (%s, %.2f ≥ %.2f) in %v — model call skipped\n",
			info.Gate, info.Label, info.Confidence, info.Floor, info.Duration.Round(time.Millisecond))
	default:
		fmt.Printf("  [gate] %s: %s at %.2f (floor %.2f) in %v — extracting as usual\n",
			info.Gate, info.Label, info.Confidence, info.Floor,
			info.Duration.Round(time.Millisecond))
	}
}

func runWithGate(ctx context.Context, engine decision.Engine) {
	// A workspace, so the second goal can actually deliver its file and the
	// delivery contract has something to check.
	workspace, err := os.MkdirTemp("", "decision-demo-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(workspace))
	if err != nil {
		log.Fatal(err)
	}

	svc, err := agent.New("decision-demo").
		WithSystemPrompt("You answer briefly.").
		// 0 takes DefaultDecisionConfidence. Raise it to skip fewer calls and
		// be surer of each skip; there is a live calibration test in
		// pkg/agent for choosing it against your own goals.
		WithDecisionEngine(engine, 0).
		WithObserver(gateWatch{}).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()
	agent.RegisterSandboxTools(svc, sb)

	// The first asks for nothing, so the gate can settle it. The second names
	// a deliverable the gate cannot describe — only a language model can say
	// which file and which tool delivers it — so the extraction runs.
	for _, goal := range []string{
		"What is a mutex? One sentence.",
		"Write a one-line summary of what a mutex is to mutex.txt",
	} {
		fmt.Printf("\n> %s\n", goal)
		res, err := svc.Run(ctx, goal)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %s\n", res.Text())
		if entries, _ := os.ReadDir(workspace); len(entries) > 0 {
			for _, e := range entries {
				fmt.Printf("  wrote %s\n", e.Name())
			}
		}
	}
}
