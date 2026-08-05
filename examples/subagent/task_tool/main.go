// Package main demonstrates v3's only composition primitive: sub-agents as a
// tool.
//
// There is no team, no dispatcher and no router in v3. You declare named
// sub-agents with WithSubagents, and the runtime exposes exactly one tool —
// `task(agent_name, prompt)` — that runs the named sub-agent through the same
// loop and returns only its final answer. Because it is an ordinary tool call,
// everything the loop provides still applies: events bubble up, output lints
// run, and terminal checkpoints are written.
//
// Usage:
//
//	go run ./examples/subagent/task_tool
//	go run ./examples/subagent/task_tool "Draft a short release note for v3."
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/poolsvc"
)

func main() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	pool := poolsvc.Global()
	if err := pool.Initialize(ctx, cfg); err != nil {
		log.Fatalf("init pool: %v", err)
	}
	llm, err := pool.GetLLMService()
	if err != nil {
		log.Fatalf("no LLM configured: %v", err)
	}

	svc, err := agent.New("lead").
		WithConfig(cfg).
		WithLLM(llm).
		WithSystemPrompt(
			"You coordinate work. When a piece of work clearly belongs to one of your "+
				"sub-agents, hand it over with the `task` tool and then present the result. "+
				"Do not narrate that you are delegating — just do it and answer.").
		WithSubagents(
			agent.SubagentSpec{
				Name:         "researcher",
				Description:  "Gathers and summarises background information on a topic.",
				Instructions: "You research a topic and return a tight, factual brief. No preamble.",
				MaxTurns:     8,
			},
			agent.SubagentSpec{
				Name:         "writer",
				Description:  "Turns notes or a brief into finished prose.",
				Instructions: "You write clear, plain prose from the notes you are given. No filler.",
				MaxTurns:     6,
			},
		).
		Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	goal := "Research what changed in AgentGo v3 and write a two-paragraph summary."
	if len(os.Args) > 1 {
		goal = strings.Join(os.Args[1:], " ")
	}

	fmt.Printf("goal: %s\n\n", goal)

	events, err := svc.RunStream(ctx, goal)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	for evt := range events {
		switch evt.Type {
		case agent.EventTypeToolCall:
			if evt.ToolName == "task" {
				fmt.Printf("→ delegating to %v\n", evt.ToolArgs["agent_name"])
			}
		case agent.EventTypeComplete:
			fmt.Printf("\n%s\n", evt.Content)
		case agent.EventTypeBlocked:
			fmt.Printf("\nblocked: %s\n", evt.Content)
		}
	}
}
