package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// codeReplyLLM always answers with the same PTC code block, so Service.Run
// takes the runPTCExecution branch.
type codeReplyLLM struct {
	mu   sync.Mutex
	code string
}

func (c *codeReplyLLM) reply() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

func (c *codeReplyLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return c.reply(), nil
}

func (c *codeReplyLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (c *codeReplyLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: c.reply()}, nil
}

func (c *codeReplyLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return callback(&domain.GenerationResult{Content: c.reply()})
}

func (c *codeReplyLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return structuredJSON(map[string]interface{}{}), nil
}

func (c *codeReplyLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// Runtime.Run installs the discovery budget, but Service.Run is a separate
// entry point building its own runCtx — and it is the one that reaches
// runPTCExecution. A caller-supplied budget must survive into the sandbox, so
// that nesting (Service.Run inside a run, subagents, PTC) shares one
// allowance instead of silently resetting it.
func TestServiceRunPropagatesCallerDiscoveryBudgetIntoPTC(t *testing.T) {
	t.Parallel()

	llm := &codeReplyLLM{code: "<code>\n" + `
var out = [];
var queries = ['send email', 'email', 'mail sender'];
for (var i = 0; i < queries.length; i++) {
  out.push(JSON.stringify(callTool('search_available_tools', {query: queries[i]})));
}
return out.join('\n---\n');
` + "\n</code>"}

	svc, err := New("service-run-budget").
		WithPTC(true).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	budget := newDiscoveryBudget(3)
	ctx := withDiscoveryBudget(context.Background(), budget)

	if _, err := svc.Run(ctx, "find me a way to send an email"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// The sandbox ran 3 distinct searches, so the run's allowance is spent.
	if v := budget.admit("something else entirely"); v != discoveryExhausted {
		t.Fatalf("verdict after the run = %v, want discoveryExhausted; the caller's budget never reached the sandbox", v)
	}
}

// The common case is a caller that supplies a plain context. Service.Run must
// establish a budget itself, or PTC search loops stay unbounded on the
// non-streaming path even though Runtime.Run is covered.
func TestServiceRunEstablishesDiscoveryBudgetWhenCallerHasNone(t *testing.T) {
	t.Parallel()

	llm := &codeReplyLLM{code: "<code>\nreturn JSON.stringify(callTool('budget_probe', {}));\n</code>"}

	svc, err := New("service-run-budget-default").
		WithPTC(true).
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
		t.Fatal("Service.Run did not install a discovery budget, so PTC searches are unbounded on this path")
	}
}
