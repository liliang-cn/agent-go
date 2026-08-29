// Package main runs one task across many segments — the shape a task that
// takes hours actually has.
//
// A single run cannot last that long. The context window fills, the round
// budget runs out, the process gets restarted. So a long task is many runs,
// and RunSegments is the loop that drives them: run, read why it stopped, run
// again.
//
// What carries across segments is deliberately not the conversation. Each
// segment starts a fresh session, so its context length starts at zero instead
// of inheriting a summary of a summary. Continuity comes from what was
// actually established — the plan (with each step's note saying what it
// produced), the workspace, and run memory. That is why segment forty reads
// the same kind of prompt segment two did.
//
// The two numbers to think about:
//
//   - RoundsPerSegment: how much one conversation is allowed to grow before
//     the task is handed over to a fresh one.
//   - MaxSegments: the backstop. MaxSegments × RoundsPerSegment is the total,
//     and running out of it is reported as exhaustion, never as an answer.
//
// Usage:
//
//	go run examples/segmented-run/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

type memoryPlanStore struct{ plans map[string][]agent.PlanItem }

func (m *memoryPlanStore) LoadPlan(_ context.Context, key string) ([]agent.PlanItem, error) {
	return m.plans[key], nil
}

func (m *memoryPlanStore) SavePlan(_ context.Context, key string, items []agent.PlanItem) error {
	if m.plans == nil {
		m.plans = map[string][]agent.PlanItem{}
	}
	m.plans[key] = items
	return nil
}

func main() {
	svc, err := agent.New("segmented").
		// The scratchpad is what makes the hand-off work: the plan, and each
		// step's note, are the only things the next segment gets.
		WithAutonomy(agent.AutonomyProfile{Scratchpad: true}).
		WithPlanStore(&memoryPlanStore{}).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	result, err := svc.RunSegments(ctx,
		"Work through this in steps. Keep a scratchpad plan, and when you finish a "+
			"step record what it produced as its note — that note is all the next "+
			"stretch of work will have to go on.",
		agent.LongRunConfig{
			MaxSegments:      40,
			RoundsPerSegment: 60,
			// Three failed segments in a row is an outage, not bad luck.
			MaxConsecutiveFailures: 3,
		})
	if err != nil {
		log.Fatalf("segments: %v", err)
	}

	// Done() is the only thing that means the work is finished. A segment
	// budget that ran out also has text, and that text is whatever the last
	// segment managed to assemble — not an answer.
	fmt.Printf("stop: %s (done: %v) after %d segments in %s\n\n",
		result.Stop, result.Done(), len(result.Segments), result.Duration.Round(time.Second))

	for _, seg := range result.Segments {
		status := string(seg.StopReason)
		if seg.Error != "" {
			status = "failed: " + seg.Error
		}
		fmt.Printf("  segment %2d  %-14s %s\n", seg.Index, status, seg.Duration.Round(time.Second))
	}

	if result.Done() {
		fmt.Printf("\n%s\n", result.Text)
		return
	}
	// An unfinished task reports how far it got rather than pretending.
	fmt.Printf("\nnot finished. the plan stands at:\n%s\n", result.PlanSummary)
}
