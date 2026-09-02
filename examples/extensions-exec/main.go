// Package main runs an extension that is not written in Go.
//
// plugins/redact.py is an ordinary Python program — stdlib only, no
// dependency on this framework and no idea what a Go interface is. It reads
// one JSON object per line and writes one back. exec.New starts it, performs
// the handshake, and presents it to the framework as an ordinary
// agent.Extension, so nothing in the loop changes.
//
// The plugin declares two capabilities: it masks email addresses in tool
// results before the model sees them, and it rejects a final answer that
// quotes one. The agent is given a tool that returns a record full of
// addresses, so both fire in one run.
//
// Usage, from the repository root:
//
//	go run ./examples/extensions-exec
//	go run ./examples/extensions-exec -plugin /path/to/redact.py -python python3
//
// The provider comes from AGENTGO_HOME (default ~/.agentgo), as in
// quickstart. Watch stderr: the plugin's own diagnostics are forwarded to the
// framework logger, named, line by line.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensions/exec"
)

func main() {
	plugin := flag.String("plugin", "examples/extensions-exec/plugins/redact.py", "path to the plugin script")
	python := flag.String("python", "python3", "interpreter to run the plugin with")
	flag.Parse()

	if _, err := os.Stat(*plugin); err != nil {
		log.Fatalf("plugin %s not found (%v) — run this from the repository root, or pass -plugin", *plugin, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	redact := exec.New("redact", []string{*python, *plugin},
		// A plugin that stops answering must not become a plugin that
		// silently passes everything: two seconds, then fail closed.
		exec.WithTimeout(2*time.Second),
		// One process serialises every run's requests. Two is plenty here;
		// a plugin doing real work per call wants one per concurrent run.
		exec.WithConcurrency(2),
	)

	svc, err := agent.New("exec-plugin-demo").
		WithPrompt("You are a support assistant. Answer briefly.").
		WithExtensions(redact).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	fmt.Printf("plugin declared: %v\n\n", redact.DeclaredCapabilities())

	// A tool whose output is exactly what must not reach the model raw.
	svc.AddTool("lookup_customer", "Look up a customer record by id",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"id": map[string]interface{}{"type": "string"}},
			"required":   []string{"id"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{
				"id":       args["id"],
				"name":     "Alice Carter",
				"email":    "alice.carter@example.com",
				"fallback": "reachable at a.carter@corp.example.org on weekends",
				"phone":    "+1 415 555 0142",
			}, nil
		})

	result, err := svc.Run(ctx, "Look up customer 42 and tell me the best way to contact them.")
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	fmt.Println("=== answer ===")
	fmt.Println(result.Text())
	fmt.Printf("stop_reason=%s blocked=%v\n", result.StopReason, result.Blocked)
}
