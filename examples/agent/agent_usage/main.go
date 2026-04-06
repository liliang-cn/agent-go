// Package main shows how to use the agentgo agent library
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func main() {
	ctx := context.Background()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Println("Creating agent...")
	svc, err := agent.New("assistant").
		WithMCP().
		WithConfig(cfg).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()
	fmt.Println("Agent created successfully")

	// Recommended tool registration: attach execution semantics so the runtime
	// can reason about concurrency, cancellation, and permission behavior.
	svc.Register(
		agent.BuildTool("workspace_summary").
			Description("Return a small summary of the current workspace target.").
			ReadOnly(true).
			InterruptBehavior(agent.InterruptBehaviorCancel).
			Handler(func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				return map[string]any{
					"workspace": "current project",
					"mode":      "demo",
				}, nil
			}).
			Build(),
	)
	fmt.Println("Registered custom tool with runtime metadata")

	fmt.Println("Planning...")
	plan, err := svc.Plan(ctx, "写一个 Go 语言的 Hello World 程序并保存到当前目录下的 hello.go 文件中")
	if err != nil {
		log.Fatalf("Plan failed: %v", err)
	}
	fmt.Printf("Plan ID: %s\n", plan.ID)

	fmt.Println("Executing...")
	result, err := svc.Execute(ctx, plan.ID)
	if err != nil {
		log.Fatalf("Execute failed: %v", err)
	}
	fmt.Printf("Result:\n%s\n", result.Text())

	fmt.Println("✅ Done")
}
