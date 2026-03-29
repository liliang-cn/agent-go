// Package main demonstrates the simplest way to get started with AgentGo.
//
// AgentGo is a Retrieval-Augmented Generation system with built-in:
// - Document ingestion and semantic search
// - Multi-provider LLM support (OpenAI, Ollama, etc.)
// - MCP tools integration
// - Agent automation with PTC (Programmatic Tool Calling)
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
	fmt.Println("✅ Done! Explore more examples in the examples/ directory.")
}
