package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// tokenBudgetLLM answers every round with the provider usage it is given,
// and, while toolCalls is set, asks for a tool so the run would go on.
type tokenBudgetLLM struct {
	calls     int32
	toolCalls bool
}

func (l *tokenBudgetLLM) result() *domain.GenerationResult {
	atomic.AddInt32(&l.calls, 1)
	r := &domain.GenerationResult{
		Content: "working on it",
		Usage:   &domain.TokenUsage{PromptTokens: 5000, CompletionTokens: 200},
	}
	if l.toolCalls {
		r.ToolCalls = []domain.ToolCall{{ID: "c1", Type: "function", Function: domain.FunctionCall{Name: "look_around", Arguments: map[string]interface{}{}}}}
	}
	return r
}

func (l *tokenBudgetLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *tokenBudgetLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *tokenBudgetLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.result(), nil
}
func (l *tokenBudgetLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.result())
}
func (l *tokenBudgetLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *tokenBudgetLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func runForTokenBudget(t *testing.T, llm *tokenBudgetLLM, opts ...RunOption) (stop StopReason, typ EventType, content string) {
	t.Helper()
	svc, err := New("token-budget-test").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	events, err := svc.RunStreamWithOptions(context.Background(), "Keep looking around.", opts...)
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}
	for evt := range events {
		switch evt.Type {
		case EventTypeBlocked, EventTypeComplete:
			stop, typ, content = evt.StopReason, evt.Type, evt.Content
		}
	}
	return
}

// A run that keeps asking for tools stops after the round that crosses its
// token budget, and says why.
func TestRuntime_MaxBudgetTokens_StopsTheRun(t *testing.T) {
	llm := &tokenBudgetLLM{toolCalls: true}
	stop, typ, content := runForTokenBudget(t, llm, WithMaxTurns(20), WithMaxBudgetTokens(1000))
	if typ != EventTypeBlocked || stop != StopReasonMaxBudgetTokens {
		t.Fatalf("got %s / %q, want blocked with %q", typ, stop, StopReasonMaxBudgetTokens)
	}
	if !strings.Contains(content, "token budget") {
		t.Errorf("the block should say it was the token budget: %s", content)
	}
	if n := atomic.LoadInt32(&llm.calls); n != 1 {
		t.Errorf("the model was asked %d times, want 1: the cap must hold before the next round", n)
	}
}

// The budget is optional: unset, the same run goes on to its round limit.
func TestRuntime_NoTokenBudgetMeansNoCap(t *testing.T) {
	llm := &tokenBudgetLLM{toolCalls: true}
	stop, _, _ := runForTokenBudget(t, llm, WithMaxTurns(3))
	if stop == StopReasonMaxBudgetTokens {
		t.Fatal("a run with no token budget was stopped by one")
	}
	if n := atomic.LoadInt32(&llm.calls); n < 2 {
		t.Errorf("the model was asked %d times; without a budget the run should have gone on", n)
	}
}

// An answer that arrives in the round that crosses the budget is still the
// answer: there is no next round to stop.
func TestRuntime_MaxBudgetTokens_KeepsAFinalAnswer(t *testing.T) {
	stop, typ, _ := runForTokenBudget(t, &tokenBudgetLLM{}, WithMaxBudgetTokens(1000))
	if typ == EventTypeBlocked && stop == StopReasonMaxBudgetTokens {
		t.Fatal("a final answer was turned into a budget stop")
	}
}

func TestSegmentTokenBudget(t *testing.T) {
	if _, ok := segmentTokenBudget(LongRunConfig{}, &domain.TokenUsage{PromptTokens: 1 << 30}); ok {
		t.Error("no MaxTotalTokens must not cap a segment")
	}
	cfg := LongRunConfig{MaxTotalTokens: 10000}
	if left, ok := segmentTokenBudget(cfg, nil); !ok || left != 10000 {
		t.Errorf("nothing used yet: got %d %v, want 10000 true", left, ok)
	}
	if left, ok := segmentTokenBudget(cfg, &domain.TokenUsage{PromptTokens: 6000, CompletionTokens: 1000}); !ok || left != 3000 {
		t.Errorf("7000 used: got %d %v, want 3000 true", left, ok)
	}
	if _, ok := segmentTokenBudget(cfg, &domain.TokenUsage{PromptTokens: 9000, CompletionTokens: 1000}); ok {
		t.Error("a spent budget must report nothing left")
	}
}

// A task's budget ends the task as a token limit, not as a blocked segment,
// and no segment starts after it is spent.
func TestRunSegments_MaxTotalTokensEndsTheTask(t *testing.T) {
	llm := &tokenBudgetLLM{toolCalls: true}
	svc, err := New("token-budget-long").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	out, err := svc.RunSegments(context.Background(), "Keep looking around.",
		LongRunConfig{MaxSegments: 5, RoundsPerSegment: 20, MaxTotalTokens: 12000})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if out.Stop != LongRunStopTokenLimit {
		t.Fatalf("stop = %q, want %q", out.Stop, LongRunStopTokenLimit)
	}
	if out.TotalUsage == nil || out.TotalUsage.PromptTokens+out.TotalUsage.CompletionTokens < 12000 {
		t.Fatalf("total usage %+v did not reach the budget", out.TotalUsage)
	}
	if n := len(out.Segments); n != 1 {
		t.Errorf("%d segments ran; the budget is spent inside the first", n)
	}
}
