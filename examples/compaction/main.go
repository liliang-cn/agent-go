// Package main shows how a run decides when to compact, and what it does.
//
// The threshold comes from the model's context window. The bundled table knows
// a handful of models; for anything else — a gateway alias most often — state
// the window with pool.RegisterModelWindow, or the run falls back to a fixed
// 60000 tokens and says so (a log warning, CompactionInfo.ThresholdSource, and
// a Doctor check).
//
// When the history crosses it, old tool results are clipped to one-line stubs
// first — no model call. Only if that is not enough is a summary asked for,
// and a summariser that keeps failing is replaced by a mechanical summary
// rather than being asked every round. The ActivityLog prints each step.
//
// Usage:
//
//	go run ./examples/compaction
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// A gateway alias no table knows: say what it can hold.
	pool.RegisterModelWindow("gemini-3.8", pool.ModelWindow{ContextTokens: 1048576, MaxOutputTokens: 65536})
	defer pool.UnregisterModelWindow("gemini-3.8")

	fmt.Println("threshold for a 200k window:", agent.CompactionThresholdForWindow(200000, 8192))
	fmt.Println("threshold for a 32k window: ", agent.CompactionThresholdForWindow(32768, 8192))

	svc, err := agent.New("compaction-demo").
		WithObserver(agent.NewActivityLog(os.Stdout)).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	result, err := svc.Run(ctx, "List the files in the workspace, read each one, and summarise what the project does.",
		// Keep the last three tool rounds verbatim; older results become stubs.
		agent.WithCompactionClipping(3),
	)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	fmt.Println()
	fmt.Println(result.Text())
}
