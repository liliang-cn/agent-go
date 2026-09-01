package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// terminalLLM ends its one turn the way a well-behaved agent does: by calling
// task_complete — with provider-reported usage on the same stream.
type terminalLLM struct{}

func (terminalLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (terminalLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (terminalLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: "pong", FinishReason: "stop"}, nil
}
func (terminalLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{
		Usage: &domain.TokenUsage{PromptTokens: 120, CompletionTokens: 8},
		ToolCalls: []domain.ToolCall{{
			ID: "call-1", Type: "function",
			Function: domain.FunctionCall{
				Name:      "task_complete",
				Arguments: map[string]interface{}{"result": "pong"},
			},
		}},
	})
}
func (terminalLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (terminalLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

type modelEndSpy struct {
	BaseObserver
	mu      sync.Mutex
	ends    int
	nilRes  int
	tokens  int
	content string
}

func (s *modelEndSpy) OnModelEnd(_ context.Context, _ ModelInfo, res *ModelResult, _ error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ends++
	if res == nil {
		s.nilRes++
		return
	}
	s.tokens += res.TokensUsed
	s.content = res.Content
}

// A turn that ends through the task-terminal sentinel must still hand
// observers its ModelResult. It used to arrive as nil — no tokens, no
// content, on exactly the turn that carries the answer — so a chat that
// wrapped up in one turn measured as zero model turns.
func TestObserverSeesTaskTerminalTurn(t *testing.T) {
	svc, err := New("terminal-observer").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(terminalLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	spy := &modelEndSpy{}
	svc.RegisterObserver(spy)

	result, err := svc.Run(context.Background(), "Say pong.")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Success {
		t.Fatalf("run did not succeed: %+v", result)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.ends == 0 {
		t.Fatal("OnModelEnd never fired")
	}
	if spy.nilRes != 0 {
		t.Fatalf("%d of %d model turns reported a nil ModelResult", spy.nilRes, spy.ends)
	}
	if spy.tokens == 0 {
		t.Fatalf("provider-reported usage was lost: %+v", spy)
	}
}
