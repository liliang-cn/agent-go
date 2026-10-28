package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// probeLLM calls one tool with no arguments on its first turn, then answers.
type probeLLM struct {
	mu          sync.Mutex
	tool        string
	turns       int
	sawFeedback bool
}

func (p *probeLLM) next(messages []domain.Message) *domain.GenerationResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range messages {
		if strings.Contains(m.Content, "you never called it") {
			p.sawFeedback = true
		}
	}
	p.turns++
	if p.turns == 1 {
		return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
			ID: "call-1", Type: "function", Function: domain.FunctionCall{Name: p.tool},
		}}}
	}
	return &domain.GenerationResult{Content: "It failed: disk on fire."}
}

func (p *probeLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (p *probeLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (p *probeLLM) GenerateWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return p.next(m), nil
}
func (p *probeLLM) StreamWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(p.next(m))
}
func (p *probeLLM) GenerateStructured(_ context.Context, prompt string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	if strings.Contains(prompt, "report ONLY the constraints") {
		return &domain.StructuredResult{Valid: true, Raw: `{"forbid_tools":false,"deliverables":[],"requested_actions":[` +
			`{"kind":"other","description":"call ` + p.tool + ` once","satisfied_by":"` + p.tool + `","unconditional":true}]}`}, nil
	}
	return structuredJSON(map[string]interface{}{}), nil
}
func (p *probeLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// Measured against claude-opus-5-5: asked to call a tool that takes no
// arguments, the model called it and reported its error, and the action
// contract still said it "never called it" three times over. A call with no
// arguments is not dispatched while the reply streams — its arguments never
// fill in — and the path that runs it afterwards did not record the call.
func TestAToolCalledWithoutArgumentsCountsAsCalled(t *testing.T) {
	t.Parallel()
	for _, fails := range []bool{false, true} {
		llm := &probeLLM{tool: "broken"}
		svc, err := New("noargs").WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm).Build()
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		t.Cleanup(func() { svc.Close() })
		calls := 0
		svc.AddToolWithMetadata("broken", "Takes no arguments.", map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(context.Context, map[string]interface{}) (interface{}, error) {
				calls++
				if fails {
					return nil, errors.New("disk on fire")
				}
				return "ok", nil
			}, ToolMetadata{})

		if _, err := svc.Run(context.Background(), "Call broken once and tell me what it said."); err != nil {
			t.Fatalf("fails=%v Run: %v", fails, err)
		}
		if calls != 1 {
			t.Errorf("fails=%v: broken ran %d times, want 1", fails, calls)
		}
		if llm.sawFeedback {
			t.Errorf("fails=%v: the contract said broken was never called after it ran", fails)
		}
	}
}
