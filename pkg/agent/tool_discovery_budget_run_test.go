package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// toolThenTextLLM calls one tool on the first turn and answers plainly after,
// which is enough to observe what context the tool actually executes under.
type toolThenTextLLM struct {
	mu       sync.Mutex
	calls    int
	toolName string
}

func (c *toolThenTextLLM) next() *domain.GenerationResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:       "call-probe",
				Type:     "function",
				Function: domain.FunctionCall{Name: c.toolName, Arguments: map[string]interface{}{}},
			}},
		}
	}
	return &domain.GenerationResult{Content: "done"}
}

func (c *toolThenTextLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "done", nil
}

func (c *toolThenTextLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (c *toolThenTextLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return c.next(), nil
}

func (c *toolThenTextLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return callback(c.next())
}

func (c *toolThenTextLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return structuredJSON(map[string]interface{}{}), nil
}

func (c *toolThenTextLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// Tool discovery is bounded to one search per task. The budget lives in the
// context, so it has to be installed before any tool executes — otherwise a
// model that keeps rewording a failed search loops forever. The common case is
// a caller handing in a plain context, so Service.Run must establish it itself.
func TestServiceRunEstablishesDiscoveryBudgetWhenCallerHasNone(t *testing.T) {
	t.Parallel()

	llm := &toolThenTextLLM{toolName: "budget_probe"}

	svc, err := New("service-run-budget-default").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	var mu sync.Mutex
	sawBudget := false
	probed := false

	type probeArgs struct{}
	svc.Register(NewTool("budget_probe", "Reports whether a discovery budget is installed",
		func(ctx context.Context, _ *probeArgs) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			probed = true
			sawBudget = discoveryBudgetFromContext(ctx) != nil
			return "ok", nil
		}))

	if _, err := svc.Run(context.Background(), "probe the budget"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !probed {
		t.Fatal("probe tool was never called; the test cannot conclude anything")
	}
	if !sawBudget {
		t.Fatal("Service.Run did not install a discovery budget, so tool searches are unbounded on this path")
	}
}

// A budget supplied by the caller must survive into tool execution, so nesting
// (Service.Run inside a run, sub-agents) shares one allowance instead of each
// level minting a fresh one.
func TestServiceRunPropagatesCallerDiscoveryBudget(t *testing.T) {
	t.Parallel()

	llm := &toolThenTextLLM{toolName: "budget_probe"}

	svc, err := New("service-run-budget-caller").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	var mu sync.Mutex
	var seen *discoveryBudget

	type probeArgs struct{}
	svc.Register(NewTool("budget_probe", "Reports the installed discovery budget",
		func(ctx context.Context, _ *probeArgs) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			seen = discoveryBudgetFromContext(ctx)
			return "ok", nil
		}))

	callerCtx := ensureDiscoveryBudget(context.Background())
	want := discoveryBudgetFromContext(callerCtx)
	if want == nil {
		t.Fatal("ensureDiscoveryBudget did not install one")
	}

	if _, err := svc.Run(callerCtx, "probe the budget"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen != want {
		t.Fatal("the caller's discovery budget did not reach tool execution; each level minted its own")
	}
}
