// Package main shows a plan whose steps depend on each other.
//
// A plan step can name an ID and the steps it comes After. Three steps that
// can run in any order and a summary that needs all three look like this to
// the model's scratchpad_set tool:
//
//	{"steps": [
//	  {"id": "a", "text": "collect source A"},
//	  {"id": "b", "text": "collect source B"},
//	  {"id": "c", "text": "collect source C"},
//	  {"id": "sum", "text": "summarise A, B and C", "after": ["a", "b", "c"]}
//	]}
//
// What the framework does with it:
//
//   - scratchpad_set / scratchpad_add refuse a cycle, a duplicate id or an
//     after naming no step, with the offending steps in the tool result;
//   - scratchpad_check refuses the summary until a, b and c are checked;
//   - the plan handed to a resumed run marks which steps are ready;
//   - a run that used its plan cannot finish while a ready step is open.
//
// A plan with no after edges is the flat checklist it always was.
//
// The first half runs with no model: it seeds a half-finished plan and prints
// the summary a resumed run would be given. The second half asks a configured
// model to do the work.
//
// Usage:
//
//	go run ./examples/plan-dag
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
	const taskID = "plan-dag-example"
	store := &memoryPlanStore{plans: map[string][]agent.PlanItem{
		// The key an unnamed plan of this task lives under.
		"default:" + taskID: {
			{ID: "a", Text: "collect source A", Done: true, Note: "A is in a.json"},
			{ID: "b", Text: "collect source B"},
			{ID: "c", Text: "collect source C"},
			{ID: "sum", Text: "summarise A, B and C", After: []string{"a", "b", "c"}},
		},
	}}

	svc, err := agent.New("plan-dag").
		WithAutonomy(agent.AutonomyProfile{Scratchpad: true}).
		WithPlanStore(store).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	defer svc.Close()

	fmt.Println("What a resumed run is handed:")
	fmt.Println(svc.PlanSummary("default:" + taskID))
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := svc.Run(ctx,
		"Carry on with the plan: collect the remaining sources (make up short contents), "+
			"check each step off with a note, then write the summary.",
		agent.WithTaskID(taskID))
	if err != nil {
		log.Printf("run (needs a configured model): %v", err)
		return
	}
	fmt.Println("Answer:", res.Text())
	for i, it := range store.plans["default:"+taskID] {
		fmt.Printf("  %d. [%v] %s  %s\n", i, it.Done, it.Text, it.Note)
	}
}
