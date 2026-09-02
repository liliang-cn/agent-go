// Package main shows Service.Preview — what the first model turn of a run
// would receive, without sending it.
//
// A run's first request is assembled from a dozen places: the system prompt,
// recalled memory, RAG, a plan left by an earlier segment, extension context,
// a skill reminder, the filtered history, and whatever tools survived the
// allow/deny lists and the run's constraints. Preview runs that assembly and
// stops. Nothing is persisted, no run is registered, and the model is not
// called — not even for constraint extraction, whose absence the result
// reports rather than hides.
//
// It takes the same RunOptions as Run, so previewing a configured run is the
// same call with the same options.
//
// Usage:
//
//	go run ./examples/preview
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	svc, err := agent.New("preview-demo").
		WithSystemPrompt("You are a careful release engineer.").
		Build()
	if err != nil {
		log.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	svc.RegisterTool(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "read_changelog",
			Description: "reads the project changelog",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}, func(context.Context, map[string]interface{}) (interface{}, error) {
		return "v3.15.0 …", nil
	})

	const goal = "Summarise what changed since the last release."

	// 1. The turn as it stands.
	show(svc, ctx, "default", goal)

	// 2. The same turn with the tool surface narrowed — the cheapest way to
	//    check that a sub-agent's allowlist leaves it something to work with.
	show(svc, ctx, "allowlist", goal, agent.WithToolAllowlist([]string{"read_changelog"}))

	// 3. And with tools refused outright. A run that forbids tools is obeyed
	//    by withholding them, so the preview shows an empty catalogue rather
	//    than a full one plus an instruction not to use it.
	show(svc, ctx, "tools disabled", goal, agent.WithToolsDisabled())
}

func show(svc *agent.Service, ctx context.Context, label, goal string, opts ...agent.RunOption) {
	p, err := svc.Preview(ctx, goal, opts...)
	if err != nil {
		log.Fatalf("preview (%s) failed: %v", label, err)
	}

	fmt.Printf("=== %s ===\n", label)
	fmt.Printf("model            : %s\n", p.Model)
	fmt.Printf("session / task   : %s / %s\n", p.SessionID, p.TaskID)
	fmt.Printf("messages         : %d\n", len(p.Messages))
	fmt.Printf("estimated tokens : %d\n", p.EstimatedTokens)
	fmt.Printf("tools (%d)        : %s\n", len(p.Tools), strings.Join(toolNames(p.Tools), ", "))
	fmt.Printf("forbids tools    : %v\n", p.Constraints.ForbidTools)
	if p.ConstraintExtractionSkipped {
		fmt.Println("constraints      : not extracted (that would have cost a model call)")
	}
	fmt.Println("--- system prompt (first 400 chars) ---")
	fmt.Println(head(p.SystemPrompt, 400))
	fmt.Println("--- messages ---")
	for _, m := range p.Messages {
		fmt.Printf("[%s] %s\n", strings.ToUpper(m.Role), head(m.Content, 160))
	}
	fmt.Println()
}

func toolNames(tools []domain.ToolDefinition) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func head(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
