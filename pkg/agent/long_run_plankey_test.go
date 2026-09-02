package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// planWritingLLM writes a two-step plan through scratchpad_set without naming
// a list, checks step one off, then concludes — which is what a model does.
type planWritingLLM struct{ turns int32 }

func (l *planWritingLLM) reply(tools []domain.ToolDefinition) *domain.GenerationResult {
	n := atomic.AddInt32(&l.turns, 1)
	call := func(name string, args map[string]interface{}) *domain.GenerationResult {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("c%d", n), Type: "function",
			Function: domain.FunctionCall{Name: name, Arguments: args},
		}}, FinishReason: "tool_calls"}
	}
	switch n {
	case 1:
		return call("scratchpad_set", map[string]interface{}{"items": []interface{}{"step one", "step two"}})
	case 2:
		return call("scratchpad_check", map[string]interface{}{"index": float64(0), "note": "done one"})
	default:
		return &domain.GenerationResult{Content: "Stopping here.", FinishReason: "stop"}
	}
}
func (l *planWritingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *planWritingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *planWritingLLM) GenerateWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(tools), nil
}
func (l *planWritingLLM) StreamWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(tools))
}
func (l *planWritingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *planWritingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// The tools must write where the supervisor reads. RunSegments scopes the
// plan to the task id; a scratchpad_set that names no list has to land under
// that key, or the supervisor reads an empty plan, sees no unfinished steps,
// and calls the task finished after step one.
func TestScratchpadWritesToTheRunsPlanKey(t *testing.T) {
	store := &memoryPlanStore{plans: map[string][]PlanItem{}}
	// Scratchpad on: that is what registers the scratchpad_* tools the model
	// calls here. The shared helper leaves it off.
	svc, err := New("plan-key").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&planWritingLLM{}).
		WithPlanStore(store).
		WithAutonomy(AutonomyProfile{Scratchpad: true}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Do two steps.", LongRunConfig{
		MaxSegments: 1, RoundsPerSegment: 5,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}

	scoped := scratchpadDefaultKey + ":" + res.TaskID
	if _, ok := store.plans[scoped]; !ok {
		t.Fatalf("plan not written under the run's key %q; keys: %v", scoped, keysOf(store.plans))
	}
	if items := store.plans[scoped]; len(items) != 2 || !items[0].Done || items[1].Done {
		t.Fatalf("plan under %q is wrong: %+v", scoped, items)
	}
	if _, leaked := store.plans[scratchpadDefaultKey]; leaked {
		t.Fatal("plan also landed under the shared default key")
	}
	// Step two is open, so the task is not finished — the gate this exists for.
	if res.Done() {
		t.Fatalf("task reported finished with an unchecked step; stop=%s", res.Stop)
	}
}

func keysOf(m map[string][]PlanItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
