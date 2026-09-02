package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// countingExt touches every seam and counts, so a concurrent test can check
// that N runs produced exactly N of everything — nothing lost, nothing
// double-counted, no run seeing another run's goal.
type countingExt struct {
	BaseObserver
	modelTurns int64
	before     int64
	after      int64
	lints      int64
	starts     int64
	ends       int64
	mu         sync.Mutex
	goals      map[string]int
}

func (c *countingExt) Name() string { return "counting" }
func (c *countingExt) OnModelStart(context.Context, ModelInfo) {
	atomic.AddInt64(&c.modelTurns, 1)
}
func (c *countingExt) Check(string, LintContext) (bool, string) {
	atomic.AddInt64(&c.lints, 1)
	return true, ""
}
func (c *countingExt) ID() string { return "counting" }
func (c *countingExt) RegisterTools(reg *ToolRegistry) error {
	reg.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{Name: "echo", Description: "echo",
			Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}}}},
	}, func(_ context.Context, args map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"echoed": args["text"]}, nil
	}, "test")
	return nil
}
func (c *countingExt) ContributeContext(_ context.Context, in ContextInput) ([]domain.Message, error) {
	return []domain.Message{{Role: "system", Content: "CTX " + in.Goal}}, nil
}
func (c *countingExt) BeforeTool(context.Context, ToolCallInfo) (ToolVerdict, error) {
	atomic.AddInt64(&c.before, 1)
	return ToolVerdict{}, nil
}
func (c *countingExt) AfterTool(context.Context, ToolResultInfo) (interface{}, bool, error) {
	atomic.AddInt64(&c.after, 1)
	return nil, false, nil
}
func (c *countingExt) OnRunStart(_ context.Context, run RunInfo) error {
	atomic.AddInt64(&c.starts, 1)
	return nil
}
func (c *countingExt) OnRunEnd(_ context.Context, run RunInfo, out RunOutcome) {
	atomic.AddInt64(&c.ends, 1)
	c.mu.Lock()
	c.goals[run.Goal]++
	c.mu.Unlock()
}

// One service, many runs at once, one extension on every seam. Run with
// -race: the point is that a Service is meant to run tasks concurrently and
// an extension is shared by all of them.
func TestExtensionsUnderConcurrentRuns(t *testing.T) {
	const runs = 12
	ext := &countingExt{goals: map[string]int{}}
	llm := &captureStreamLLM{replies: []*domain.GenerationResult{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Type: "function", Function: domain.FunctionCall{
			Name: "echo", Arguments: map[string]interface{}{"text": "hi"},
		}}}},
		{Content: "done"},
	}}
	// The scripted LLM answers by call count, not per run; with N runs
	// interleaving, make every call beyond the first a tool call or an
	// answer in a way that still terminates: alternate deterministically
	// per invocation is not possible, so give it a long enough script.
	var script []*domain.GenerationResult
	for i := 0; i < runs; i++ {
		script = append(script, llm.replies[0], llm.replies[1])
	}
	llm.replies = script

	svc := buildWith(t, llm, ext)

	var wg sync.WaitGroup
	errs := make(chan error, runs)
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			goal := fmt.Sprintf("goal-%d", i)
			events, err := svc.RunStream(context.Background(), goal)
			if err != nil {
				errs <- err
				return
			}
			for ev := range events {
				if ev.Type == EventTypeError {
					errs <- fmt.Errorf("run %d: %s", i, ev.Content)
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got := atomic.LoadInt64(&ext.starts); got != runs {
		t.Fatalf("OnRunStart fired %d times, want %d", got, runs)
	}
	if got := atomic.LoadInt64(&ext.ends); got != runs {
		t.Fatalf("OnRunEnd fired %d times, want %d", got, runs)
	}
	if got := atomic.LoadInt64(&ext.lints); got < runs {
		t.Fatalf("lint ran %d times, want at least %d", got, runs)
	}
	if got := atomic.LoadInt64(&ext.modelTurns); got < runs {
		t.Fatalf("observer saw %d model turns, want at least %d", got, runs)
	}
	if b, a := atomic.LoadInt64(&ext.before), atomic.LoadInt64(&ext.after); b != a {
		t.Fatalf("before=%d after=%d: a tool call lost its result filter", b, a)
	}
	ext.mu.Lock()
	defer ext.mu.Unlock()
	for i := 0; i < runs; i++ {
		if ext.goals[fmt.Sprintf("goal-%d", i)] != 1 {
			t.Fatalf("goal-%d ended %d times", i, ext.goals[fmt.Sprintf("goal-%d", i)])
		}
	}
	// Each run's contributed context names its own goal, never another's.
	llm.mu.Lock()
	defer llm.mu.Unlock()
	for _, round := range llm.captured {
		var goal, ctxGoal string
		for _, m := range round {
			if strings.HasPrefix(m.Content, "CTX goal-") {
				ctxGoal = strings.TrimPrefix(m.Content, "CTX ")
			}
			if m.Role == "user" && strings.HasPrefix(m.Content, "goal-") {
				goal = m.Content
			}
		}
		if goal != "" && ctxGoal != "" && goal != ctxGoal {
			t.Fatalf("run for %s saw context for %s", goal, ctxGoal)
		}
	}
}
