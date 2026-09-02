// Package main shows extensions: one concern, one entry, every seam it needs.
//
// Three ship with the framework and are installed here together:
//
//   - logging  — the activity log, one greppable line per model turn, tool
//     call, retry, compaction and checkpoint
//   - pii      — masks personal data in tool results before the model sees
//     them, and rejects a final answer that still leaks
//   - usage    — a ledger of tokens by model with the cache split, priced
//     where a price is known
//
// The agent is given a tool that returns a customer record full of things the
// model must not repeat. Watch the log: the tool result the model receives is
// already masked, and the usage table at the end prices the run.
//
// Usage:
//
//	go run ./examples/extensions
//
// The provider comes from AGENTGO_HOME (default ~/.agentgo), as in quickstart.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/logging"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/pii"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/usage"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	meter := usage.New()
	guard := pii.New(
		// A search engine is somewhere data leaves the machine.
		pii.WithBlockedTools("web_search"),
	)

	svc, err := agent.New("extensions-demo").
		WithPrompt("You are a support assistant. Answer briefly.").
		WithExtensions(
			logging.New(os.Stderr),
			guard,
			meter,
		).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// A tool whose output is exactly what must not reach the model raw.
	svc.AddTool("lookup_customer", "Look up a customer record by id",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"id": map[string]interface{}{"type": "string"}},
			"required":   []string{"id"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{
				"id":    args["id"],
				"name":  "Alice Carter",
				"email": "alice.carter@example.com",
				"phone": "+1 415 555 0142",
				"card":  "4111 1111 1111 1111",
				"notes": "Prefers email. API key on file: sk-live-abcdefghijklmnopqrstuvwxyz",
			}, nil
		})

	result, err := svc.Run(ctx, "Look up customer 42 and tell me the best way to contact them.")
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	fmt.Println()
	fmt.Println("=== answer ===")
	fmt.Println(result.Text())
	fmt.Printf("stop_reason=%s blocked=%v\n", result.StopReason, result.Blocked)

	fmt.Println()
	fmt.Println("=== pii masked, by kind ===")
	for kind, n := range guard.Stats() {
		fmt.Printf("%-12s %d\n", kind, n)
	}

	fmt.Println()
	fmt.Println("=== usage ===")
	meter.Report(os.Stdout)
}
