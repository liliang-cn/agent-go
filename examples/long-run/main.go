// Package main shows how to configure an agent for work that takes hours
// rather than seconds.
//
// Four settings matter, and each exists because of a specific way a long run
// used to end early:
//
//   - MaxRounds. The framework default is 20, sized for a conversational turn.
//     A task with hundreds of steps needs hundreds of rounds, and until it is
//     raised the run stops partway and synthesises an answer out of what it
//     managed to reach — which looks like success and is not.
//   - CheckpointEveryRounds. Snapshots taken while the run is still going are
//     what a supervisor resumes from. Without them the only run on disk is a
//     finished one, and the run worth resuming is the one still in flight.
//   - WithLLMRetries. Over hours the provider will blink: a 502, a rate limit,
//     a dropped stream. Those are waited out with backoff rather than ending
//     the run. Genuine rejections still stop immediately.
//   - Scratchpad + WithPlanStore. The plan is what survives an interruption,
//     and PlanItem.Note is what makes it useful — "step 3 is done" is not
//     enough to carry on from, "step 3 found the port in settings.json" is.
//     A run that starts with a plan already in the store is told about it.
//
// Usage:
//
//	go run examples/long-run/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// filePlanStore would normally persist somewhere durable; the point of the
// interface is that agent-go does not care where. See cortexbridge for a
// CortexDB-backed one, or superai-desktop's GraphPlanStore.
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
	svc, err := agent.New("long-horizon").
		WithAutonomy(agent.AutonomyProfile{
			// Hundreds, not twenty.
			MaxRounds: 400,
			// Snapshot every round: a crash then costs one round, not the run.
			// Raise it to trade resume granularity for fewer writes.
			CheckpointEveryRounds: 1,
			// scratchpad_* tools, so the agent has somewhere to keep the plan.
			Scratchpad: true,
		}).
		WithPlanStore(&memoryPlanStore{}).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// A run this long should own its own deadline rather than inherit one.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	result, err := svc.Run(ctx,
		"Work through this in steps. Keep a plan with the scratchpad tools and "+
			"record what each step produced as you finish it.",
		// Per-run overrides beat the service's settings.
		agent.WithMaxTurns(400),
		// Eight attempts rides out a longer outage than the default four.
		agent.WithLLMRetries(8),
	)
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	fmt.Println(result.Text())

	// What it actually cost. Usage is nil when the provider reports no
	// accounting at all — a different thing from a run that used no tokens.
	if u := result.Usage; u != nil {
		fmt.Printf("\nprompt %d (cached %d, writes %d), completion %d, over %s\n",
			u.PromptTokens, u.CachedPromptTokens, u.CacheWriteTokens,
			u.CompletionTokens, result.Duration)
	}

	// A run interrupted before this point left its plan behind; the next run on
	// the same store is told where it got to, so it carries on instead of
	// starting over. Nothing to do but look:
	if summary := svc.PlanSummary("default"); summary != "" {
		fmt.Printf("\n%s\n", summary)
	}
}
