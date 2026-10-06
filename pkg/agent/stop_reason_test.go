package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func TestClassifyFinishReason(t *testing.T) {
	t.Parallel()
	cases := map[string]StopReason{
		"":               "",
		"stop":           "",
		"tool_calls":     "",
		"refusal":        StopReasonRefusal,
		"REFUSAL":        StopReasonRefusal,
		"content_filter": StopReasonRefusal,
		"content-filter": StopReasonRefusal,
		"safety":         StopReasonRefusal,
		"length":         StopReasonMaxTokens,
		"max_tokens":     StopReasonMaxTokens,
	}
	for in, want := range cases {
		got := classifyFinishReason(in)
		if got != want {
			t.Errorf("classifyFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikeRefusalText(t *testing.T) {
	t.Parallel()
	positives := []string{
		"I can't help with that.",
		"I cannot help you build a weapon.",
		"I'm sorry, but I can't fulfill that request.",
		"I am unable to provide that information.",
		"我无法协助这个请求。",
		"抱歉，我不能完成这个任务。",
	}
	for _, s := range positives {
		if !looksLikeRefusalText(s) {
			t.Errorf("expected refusal for: %q", s)
		}
	}
	negatives := []string{
		"",
		"Here is the answer.",
		"The capital of France is Paris.",
		"I can help with that. Step one is to ...",
	}
	for _, s := range negatives {
		if looksLikeRefusalText(s) {
			t.Errorf("did not expect refusal for: %q", s)
		}
	}
}

// refusalLLM emits a clean refusal text on its first call. Used to verify
// the runtime surfaces StopReasonRefusal on completion.
type refusalLLM struct {
	mu      sync.Mutex
	replies []*domain.GenerationResult
	calls   int32
}

func (l *refusalLLM) next() *domain.GenerationResult {
	idx := int(atomic.AddInt32(&l.calls, 1)) - 1
	if idx >= len(l.replies) {
		idx = len(l.replies) - 1
	}
	if idx < 0 {
		return &domain.GenerationResult{}
	}
	r := *l.replies[idx]
	return &r
}
func (l *refusalLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *refusalLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	return nil
}
func (l *refusalLLM) GenerateWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.next(), nil
}
func (l *refusalLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.next())
}
func (l *refusalLLM) GenerateStructured(ctx context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *refusalLLM) RecognizeIntent(ctx context.Context, _ string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestRuntime_StopReason_RefusalViaFinishReason(t *testing.T) {
	llm := &refusalLLM{
		replies: []*domain.GenerationResult{
			{Content: "Sorry, that's outside what I'll do.", FinishReason: "refusal"},
		},
	}
	svc, err := New("refusal-finish-reason-test").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Do something disallowed.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var sawComplete bool
	var stopReason StopReason
	for evt := range events {
		if evt.Type == EventTypeComplete {
			sawComplete = true
			stopReason = evt.StopReason
		}
	}
	if !sawComplete {
		t.Fatal("expected a workflow_complete event")
	}
	if stopReason != StopReasonRefusal {
		t.Fatalf("stop_reason = %q, want %q", stopReason, StopReasonRefusal)
	}
}

func TestRuntime_StopReason_RefusalViaHeuristic(t *testing.T) {
	// No FinishReason set — runtime must fall back to the content
	// heuristic and still classify this as a refusal.
	llm := &refusalLLM{
		replies: []*domain.GenerationResult{
			{Content: "I cannot help with that request."},
		},
	}
	svc, err := New("refusal-heuristic-test").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Do something disallowed.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var stopReason StopReason
	for evt := range events {
		if evt.Type == EventTypeComplete {
			stopReason = evt.StopReason
		}
	}
	if stopReason != StopReasonRefusal {
		t.Fatalf("stop_reason = %q, want %q", stopReason, StopReasonRefusal)
	}
}

func TestRuntime_StopReason_NormalEndTurn(t *testing.T) {
	llm := &refusalLLM{
		replies: []*domain.GenerationResult{
			{Content: "Here is the answer: 42.", FinishReason: "stop"},
		},
	}
	svc, err := New("normal-end-turn-test").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "What is the answer?")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var stopReason StopReason
	for evt := range events {
		if evt.Type == EventTypeComplete {
			stopReason = evt.StopReason
		}
	}
	if stopReason != StopReasonEndTurn {
		t.Fatalf("stop_reason = %q, want %q", stopReason, StopReasonEndTurn)
	}
}
