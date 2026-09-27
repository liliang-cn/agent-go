// Package main shows parallel sub-agent fan-out through the `task` tool.
//
// A sub-agent is a tool call, and by default a `task` call runs alone: a
// child can write the workspace its siblings are using, so nothing overlaps
// it. A spec that only reads (or works in its own worktree) declares
// Parallel, and then several `task` calls in one turn run side by side, at
// most WithSubagentMaxParallel at a time. SubagentSpec.Model sends the
// child's requests to a different model through the same per-run model
// field WithModel uses. WithSubagentMaxDepth bounds nesting: a child at the
// bound is not offered `task`, and a call it makes anyway returns a refusal
// telling it to do the work itself.
//
// The model here is scripted and every turn takes 300ms, so the run needs no
// provider and the timing is the point: the same four-way fan-out is run
// once with a serial spec and once with a parallel one.
//
// Usage:
//
//	go run ./examples/subagent/fanout
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func main() {
	for _, parallel := range []bool{false, true} {
		elapsed, err := fanOut(parallel)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Parallel=%-5v  4 sub-agents, 300ms per model turn: %v\n", parallel, elapsed.Round(10*time.Millisecond))
	}
}

func fanOut(parallel bool) (time.Duration, error) {
	home, err := os.MkdirTemp("", "agentgo-fanout-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(home)
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()

	svc, err := agent.New("lead").
		WithConfig(cfg).
		WithLLM(&scriptedLLM{latency: 300 * time.Millisecond}).
		WithSubagents(agent.SubagentSpec{
			Name:         "reader",
			Description:  "Reads one source and summarises it. Read-only.",
			Instructions: "Read the source you are given and summarise it in one line.",
			MaxTurns:     4,
			Parallel:     parallel,        // read-only children may overlap
			Model:        "cheap-summary", // routed like WithModel; the pool falls back if it has no such client
		}).
		WithSubagentMaxParallel(4).
		WithSubagentMaxDepth(1). // the default, stated: children may not delegate further
		Build()
	if err != nil {
		return 0, fmt.Errorf("build: %w", err)
	}
	defer svc.Close()

	started := time.Now()
	events, err := svc.RunStreamWithOptions(context.Background(), "Summarise sources A, B, C and D.",
		agent.WithConstraintExtraction(false))
	if err != nil {
		return 0, err
	}
	children := map[string]bool{}
	var answer string
	for evt := range events {
		// Parallel children share one event channel; SubAgentID says whose
		// line each event is.
		if evt.SubAgentID != "" {
			children[evt.SubAgentID] = true
		}
		if evt.Type == agent.EventTypeComplete && evt.SubAgentID == "" {
			answer = evt.Content
		}
	}
	elapsed := time.Since(started)
	fmt.Printf("  answer: %q (events from %d distinct sub-agents)\n", answer, len(children))
	return elapsed, nil
}

// scriptedLLM plays a lead that fans out four `task` calls and children that
// answer in one turn. It tells them apart by the sub-agent rules appended to
// every child's system prompt.
type scriptedLLM struct{ latency time.Duration }

func (s *scriptedLLM) turn(ctx context.Context, messages []domain.Message, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	select {
	case <-time.After(s.latency):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	system, results := "", 0
	for _, m := range messages {
		if m.Role == "system" && system == "" {
			system = m.Content
		}
		if m.Role == "tool" {
			results++
		}
	}
	if strings.Contains(system, "Sub-Agent Execution Rules") {
		model := ""
		if opts != nil {
			model = opts.Model
		}
		return &domain.GenerationResult{Content: "summary written by " + model}, nil
	}
	if results == 0 {
		var calls []domain.ToolCall
		for _, src := range []string{"A", "B", "C", "D"} {
			calls = append(calls, domain.ToolCall{
				ID: "call_" + src, Type: "function",
				Function: domain.FunctionCall{Name: "task", Arguments: map[string]interface{}{
					"agent_name": "reader", "prompt": "Summarise source " + src + ".",
				}},
			})
		}
		return &domain.GenerationResult{ToolCalls: calls}, nil
	}
	return &domain.GenerationResult{Content: fmt.Sprintf("Combined %d summaries.", results)}, nil
}

func (s *scriptedLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (s *scriptedLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, cb func(string)) error {
	return nil
}

func (s *scriptedLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return s.turn(ctx, messages, opts)
}

func (s *scriptedLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := s.turn(ctx, messages, opts)
	if err != nil {
		return err
	}
	return cb(res)
}

func (s *scriptedLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Raw: `{}`, Valid: true}, nil
}

func (s *scriptedLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}
