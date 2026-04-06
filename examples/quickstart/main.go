// Package main demonstrates the simplest way to get started with AgentGo.
//
// AgentGo is a Go framework centered on Agent / Team runtimes.
// Capabilities such as RAG, memory, MCP, skills, and PTC can be attached
// to the same framework core as needed.
//
// Usage:
//
//	go run examples/quickstart/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("=== AgentGo Quickstart ===")
	fmt.Println()

	// Create an agent - runtime configuration is loaded from AGENTGO_HOME + agentgo.db
	svc, err := agent.New("quickstart").Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	// Ask() — the simplest API: returns (string, error) directly.
	fmt.Println("Q: What can you help me with?")
	fmt.Print("A: ")

	reply, err := svc.Ask(ctx, "What can you help me with? Give a brief answer.")
	if err != nil {
		log.Fatalf("Failed: %v", err)
	}
	fmt.Println(reply)

	fmt.Println()

	// Chat() — multi-turn conversation, returns *ExecutionResult with
	// session ID, RAG sources, PTC details. Use result.Text() for the reply.
	fmt.Println("Q: What is 2+2?")
	fmt.Print("A: ")

	result, err := svc.Chat(ctx, "What is 2+2? Answer in one sentence.")
	if err != nil {
		log.Fatalf("Failed: %v", err)
	}
	fmt.Println(result.Text())

	fmt.Println()

	// Stream() — token-by-token streaming. The simplest streaming API.
	// Returns <-chan string; loop over it to print tokens as they arrive.
	fmt.Println("Q: Count from 1 to 5 (streamed):")
	fmt.Print("A: ")

	for token := range svc.Stream(ctx, "Count from 1 to 5, one number per line.") {
		fmt.Print(token)
	}
	fmt.Println()

	fmt.Println()
	// RunStream() — full runtime event visibility. This is the API to use when
	// you want turn stages, tool calls, tool states, and final completion events.
	fmt.Println("Q: Briefly explain when you would use tools.")
	fmt.Println("Runtime:")

	events, err := svc.RunStream(ctx, "Briefly explain when you would use tools.")
	if err != nil {
		log.Fatalf("Failed to start RunStream: %v", err)
	}
	for evt := range events {
		switch evt.Type {
		case agent.EventTypeStateUpdate:
			fmt.Printf("  [state] %v\n", evt.StateDelta)
		case agent.EventTypePartial:
			fmt.Print(evt.Content)
		case agent.EventTypeToolCall:
			fmt.Printf("\n  [tool_call] %s %v\n", evt.ToolName, evt.ToolArgs)
		case agent.EventTypeToolResult:
			fmt.Printf("\n  [tool_result] %s => %v\n", evt.ToolName, evt.ToolResult)
		}
	}
	fmt.Println()

	fmt.Println()
	fmt.Println("✅ Done! Explore more examples in the examples/ directory.")
}
