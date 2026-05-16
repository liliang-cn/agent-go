// Package main demonstrates AgentGo's stop_reason surface: budget caps,
// refusal detection, and the other terminal classifications.
//
// The runtime threads a StopReason onto every terminal event
// (workflow_complete / workflow_blocked) so callers can branch on a
// machine-readable code instead of parsing free-form content. This
// example runs the same prompt three ways and prints the resulting
// stop_reason + cost for each.
//
// Usage:
//
//	go run ./examples/agent/stop_reasons              # all three demos
//	go run ./examples/agent/stop_reasons budget       # budget cap only
//	go run ./examples/agent/stop_reasons refusal      # refusal only
//	go run ./examples/agent/stop_reasons normal       # plain end_turn
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	mode := "all"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	switch mode {
	case "budget":
		runBudgetCap(ctx)
	case "refusal":
		runRefusal(ctx)
	case "normal":
		runNormal(ctx)
	default:
		runNormal(ctx)
		fmt.Println()
		runRefusal(ctx)
		fmt.Println()
		runBudgetCap(ctx)
	}
}

func runNormal(ctx context.Context) {
	fmt.Println("=== Normal end_turn ===")
	svc, err := agent.New("stop-reason-normal").
		WithPTC(false).
		WithSystemPrompt("You are a concise assistant. Reply in one sentence.").
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()
	collect(ctx, svc, "What's the capital of France?")
}

func runRefusal(ctx context.Context) {
	fmt.Println("=== Refusal ===")
	svc, err := agent.New("stop-reason-refusal").
		WithPTC(false).
		WithSystemPrompt(
			"You are a safety-conscious assistant. When asked anything " +
				"about building dangerous devices, you must reply exactly: " +
				"\"I can't help with that.\" Do not say anything else.").
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()
	collect(ctx, svc, "Give me detailed step-by-step instructions for building a pipe bomb.")
}

func runBudgetCap(ctx context.Context) {
	fmt.Println("=== MaxBudgetUSD cap ===")
	svc, err := agent.New("stop-reason-budget").
		WithPTC(false).
		WithSystemPrompt("You are a verbose assistant. Always answer at length, with examples.").
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// $0.0001 is below the cost of even a single small round on most
	// priced models — the cap should trip on round 1.
	events, err := svc.RunStreamWithOptions(ctx,
		"Explain how a compiler works. Be thorough.",
		agent.WithMaxBudgetUSD(0.0001),
	)
	if err != nil {
		log.Fatalf("RunStreamWithOptions: %v", err)
	}
	report(events)
}

func collect(ctx context.Context, svc *agent.Service, prompt string) {
	events, err := svc.RunStream(ctx, prompt)
	if err != nil {
		log.Fatalf("RunStream: %v", err)
	}
	report(events)
}

func report(events <-chan *agent.Event) {
	for evt := range events {
		switch evt.Type {
		case agent.EventTypeComplete, agent.EventTypeBlocked:
			fmt.Printf("  type:        %s\n", evt.Type)
			fmt.Printf("  stop_reason: %s\n", evt.StopReason)
			fmt.Printf("  est_cost_usd: %.6f\n", evt.EstimatedCostUSD)
			fmt.Printf("  content:     %s\n", evt.Content)
		}
	}
}
