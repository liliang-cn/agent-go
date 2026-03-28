// Package main demonstrates the simplest library path for direct agent dispatch.
//
// Usage:
//
//	go run ./examples/team_local_dispatch
//
// Optional:
//
//	AGENTGO_EXAMPLE_HOME=/tmp/agentgo-demo go run ./examples/team_local_dispatch
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v2/examples/internal/teamdemo"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	manager, cfg, err := teamdemo.NewManager("team-local-dispatch")
	if err != nil {
		log.Fatalf("setup manager: %v", err)
	}

	fmt.Println("=== Team Local Dispatch Example ===")
	fmt.Printf("Example home: %s\n", cfg.Home)
	fmt.Println("Dispatching directly to Assistant...")

	reply, err := manager.DispatchTask(ctx, "Assistant", "Reply with exactly: LOCAL_DISPATCH_OK")
	if err != nil {
		log.Fatalf("dispatch task: %v", err)
	}

	status, err := manager.GetAgentStatus("Assistant")
	if err != nil {
		log.Fatalf("get agent status: %v", err)
	}

	fmt.Printf("Assistant reply: %s\n", reply)
	fmt.Printf("Runtime mode: %s | Workers: %d | Queue depth: %d | Processed: %d\n",
		status.RuntimeMode, status.WorkerCount, status.QueueDepth, status.ProcessedMessages)
}
