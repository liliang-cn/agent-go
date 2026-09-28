package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// planActor is a scripted model working a four-step plan — three steps that
// can run in any order and a summary that needs all three — and trying to
// stop at chosen points. Each call plays the next action; a finish the runtime
// rejects is simply followed by the next action, which is what a model told
// "you are not done" does.
type planActor struct {
	mu      sync.Mutex
	actions []string // "set", "check:N", "finish"
	next    int
	// refusedChecks counts scratchpad_check calls whose result said no.
	refusedChecks int
	finishes      int
}

func (a *planActor) reply(messages []domain.Message, tools []domain.ToolDefinition) *domain.GenerationResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Refusals are counted over the whole transcript each turn (the runtime
	// appends a nudge after tool results, so the last message is not the
	// tool's); the largest count seen is the run's.
	refused := 0
	for _, m := range messages {
		if m.Role == "tool" && strings.Contains(m.Content, "cannot be checked yet") {
			refused++
		}
	}
	if refused > a.refusedChecks {
		a.refusedChecks = refused
	}
	if len(tools) == 0 || a.next >= len(a.actions) {
		a.finishes++
		return &domain.GenerationResult{Content: "Finished: the report is summarised above.", FinishReason: "stop"}
	}
	act := a.actions[a.next]
	a.next++
	call := func(name string, args map[string]interface{}) *domain.GenerationResult {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("c%d", a.next), Type: "function",
			Function: domain.FunctionCall{Name: name, Arguments: args},
		}}, FinishReason: "tool_calls"}
	}
	switch {
	case act == "set":
		if toolHasProperty(tools, "scratchpad_set", "steps") {
			return call("scratchpad_set", map[string]interface{}{"steps": []interface{}{
				map[string]interface{}{"id": "a", "text": "collect source A"},
				map[string]interface{}{"id": "b", "text": "collect source B"},
				map[string]interface{}{"id": "c", "text": "collect source C"},
				map[string]interface{}{"id": "summary", "text": "summarise A, B and C", "after": []interface{}{"a", "b", "c"}},
			}})
		}
		return call("scratchpad_set", map[string]interface{}{"items": []interface{}{
			"collect source A", "collect source B", "collect source C", "summarise A, B and C",
		}})
	case strings.HasPrefix(act, "check:"):
		var idx int
		fmt.Sscanf(strings.TrimPrefix(act, "check:"), "%d", &idx)
		return call("scratchpad_check", map[string]interface{}{"index": float64(idx), "note": "done " + act})
	default: // finish
		a.finishes++
		return &domain.GenerationResult{Content: "Finished: the report is summarised above.", FinishReason: "stop"}
	}
}

func toolHasProperty(tools []domain.ToolDefinition, name, prop string) bool {
	for _, t := range tools {
		if t.Function.Name != name {
			continue
		}
		props, _ := t.Function.Parameters["properties"].(map[string]interface{})
		_, ok := props[prop]
		return ok
	}
	return false
}

func (a *planActor) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (a *planActor) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (a *planActor) GenerateWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return a.reply(m, t), nil
}
func (a *planActor) StreamWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(a.reply(m, t))
}
func (a *planActor) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (a *planActor) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

type planDAGOutcome struct {
	Status        string // completed | blocked
	Unchecked     int
	Finishes      int
	RefusedChecks int
	// Premature is a run that reported completion with plan steps left open.
	Premature bool
}

func runPlanActor(t *testing.T, actions []string) planDAGOutcome {
	t.Helper()
	actor := &planActor{actions: actions}
	svc, err := New("plan-dag").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(actor).
		WithPlanStore(&memoryPlanStore{plans: map[string][]PlanItem{}}).
		WithAutonomy(AutonomyProfile{Scratchpad: true}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	taskID := "task-" + strings.ReplaceAll(t.Name(), "/", "-")
	res, err := svc.Run(context.Background(), "Collect three sources and summarise them.",
		WithTaskID(taskID), WithMaxTurns(20), WithConstraintExtraction(false))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	out := planDAGOutcome{Status: "completed"}
	if res.Blocked {
		out.Status = "blocked"
	}
	for _, it := range svc.scratchpadStore().get(taskScopedPlanKey(taskID)) {
		if !it.Done {
			out.Unchecked++
		}
	}
	actor.mu.Lock()
	out.Finishes, out.RefusedChecks = actor.finishes, actor.refusedChecks
	actor.mu.Unlock()
	out.Premature = out.Status == "completed" && out.Unchecked > 0
	return out
}

// planDAGVariants are the ways a model gets a dependent plan wrong, plus one
// that gets it right as the control for false positives.
var planDAGVariants = []struct {
	name    string
	actions []string
}{
	{"stops_after_two", []string{"set", "check:0", "check:1", "finish", "check:2", "check:3", "finish"}},
	{"summary_first", []string{"set", "check:3", "check:0", "finish", "check:1", "check:2", "check:3", "finish"}},
	{"stops_after_three", []string{"set", "check:0", "check:1", "check:2", "finish", "check:3", "finish"}},
	{"eager_every_step", []string{"set", "check:0", "finish", "check:1", "finish", "check:2", "finish", "check:3", "finish"}},
	{"honest_control", []string{"set", "check:0", "check:1", "check:2", "check:3", "finish"}},
}

// TestPlanDAGPrematureFinishMeasure is the AG2 measurement: a dependent plan
// and a model that tries to stop early. Run with -v to see the table.
//
// Measured on the code before plans were graphs: 4 of 5 runs completed with
// steps open (the control was the fifth), and "summary_first" checked the
// summary with all three of its inputs unfinished, unrefused.
func TestPlanDAGPrematureFinishMeasure(t *testing.T) {
	premature := 0
	for _, v := range planDAGVariants {
		v := v
		t.Run(v.name, func(t *testing.T) {
			o := runPlanActor(t, v.actions)
			if o.Premature {
				premature++
			}
			t.Logf("MEASURE %-18s status=%-9s unchecked=%d finishes=%d refused_checks=%d premature=%v",
				v.name, o.Status, o.Unchecked, o.Finishes, o.RefusedChecks, o.Premature)
			if o.Premature {
				t.Errorf("%s finished with %d plan step(s) open", v.name, o.Unchecked)
			}
			switch v.name {
			case "honest_control":
				// No false positive: a model that did the work finishes first time.
				if o.Status != "completed" || o.Finishes != 1 {
					t.Errorf("control was held back: %+v", o)
				}
			case "summary_first":
				if o.RefusedChecks != 1 {
					t.Errorf("checking the summary before its inputs was not refused: %+v", o)
				}
			case "eager_every_step":
				// Three early finishes against a retry budget of two: the run
				// blocks rather than completing with work left, which is the
				// honest outcome.
				if o.Status != "blocked" {
					t.Errorf("eager finisher: %+v", o)
				}
			default:
				if o.Status != "completed" || o.Unchecked != 0 {
					t.Errorf("%s did not recover to a finished plan: %+v", v.name, o)
				}
			}
		})
	}
	t.Logf("MEASURE premature_finishes=%d of %d runs", premature, len(planDAGVariants))
}
