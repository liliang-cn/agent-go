package agent

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

type serviceExecutionStateTestLLM struct {
	results       []*domain.GenerationResult
	generateCalls int
}

func (l *serviceExecutionStateTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *serviceExecutionStateTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (l *serviceExecutionStateTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	if l.generateCalls >= len(l.results) {
		return nil, fmt.Errorf("unexpected GenerateWithTools call %d", l.generateCalls+1)
	}
	result := l.results[l.generateCalls]
	l.generateCalls++
	if result == nil {
		return nil, nil
	}
	cloned := *result
	cloned.ToolCalls = append([]domain.ToolCall(nil), result.ToolCalls...)
	return &cloned, nil
}

func (l *serviceExecutionStateTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (l *serviceExecutionStateTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return nil, nil
}

func (l *serviceExecutionStateTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestServiceExecutionLoopState_TracksTransitionAndMetrics(t *testing.T) {
	state := newServiceExecutionLoopState(
		"inspect repo",
		[]domain.Message{{Role: "user", Content: "inspect repo"}},
		&IntentRecognitionResult{Transition: "tool_first"},
		3,
		NewAgent("Assistant"),
	)

	state.beginRound()
	state.noteTurnTokens(42)
	state.noteToolResults([]ToolExecutionResult{{ToolName: "read_file"}})
	nextMessages := append(state.Messages, domain.Message{Role: "tool", Content: "ok"})
	state.continueWith(queryLoopTransitionToolBatch, "tool batch completed; continue to next turn", nextMessages)

	if state.LoopTransition != queryLoopTransitionToolBatch {
		t.Fatalf("loop transition = %q", state.LoopTransition)
	}
	if state.Transition != "tool_first" {
		t.Fatalf("intent transition = %q", state.Transition)
	}
	if state.Budget.CompletedRounds != 1 {
		t.Fatalf("completed rounds = %d, want 1", state.Budget.CompletedRounds)
	}
	if state.TotalToolCalls != 1 {
		t.Fatalf("total tool calls = %d, want 1", state.TotalToolCalls)
	}

	metrics := state.metricsSnapshot()
	if metrics.toolCalls != 1 {
		t.Fatalf("metrics.toolCalls = %d, want 1", metrics.toolCalls)
	}
	if metrics.estimatedTokens != 42 {
		t.Fatalf("metrics.estimatedTokens = %d, want 42", metrics.estimatedTokens)
	}
	if len(metrics.toolsUsed) != 1 || metrics.toolsUsed[0] != "read_file" {
		t.Fatalf("metrics.toolsUsed = %#v", metrics.toolsUsed)
	}
}

func TestExecuteWithLLM_StateMachineCarriesToolRoundForward(t *testing.T) {
	llm := &serviceExecutionStateTestLLM{
		results: []*domain.GenerationResult{
			{
				Content: "Calling the tool.",
				ToolCalls: []domain.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: domain.FunctionCall{
							Name: "echo_tool",
							Arguments: map[string]interface{}{
								"msg": "hello",
							},
						},
					},
				},
			},
			{
				Content: "done",
			},
		},
	}

	agent := NewAgent("Assistant")
	agent.AddToolWithMetadata(
		"echo_tool",
		"Echo input",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"msg": map[string]interface{}{"type": "string"},
			},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return fmt.Sprintf("echo:%v", args["msg"]), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true},
	)

	svc := &Service{
		llmService:      llm,
		agent:           agent,
		registry:        NewRegistry(),
		logger:          slog.Default(),
		promptManager:   prompt.NewManager(),
		toolRegistry:    NewToolRegistry(),
		inProgressTools: make(map[string]int),
	}
	svc.registry.Register(agent)

	result, metrics, err := svc.executeWithLLM(context.Background(), "inspect repo", nil, NewSession(agent.ID()), "", "", DefaultRunConfig())
	if err != nil {
		t.Fatalf("executeWithLLM error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %#v, want %q", result, "done")
	}
	if llm.generateCalls != 2 {
		t.Fatalf("GenerateWithTools calls = %d, want 2", llm.generateCalls)
	}
	if metrics.toolCalls != 1 {
		t.Fatalf("metrics.toolCalls = %d, want 1", metrics.toolCalls)
	}
	if len(metrics.toolsUsed) != 1 || metrics.toolsUsed[0] != "echo_tool" {
		t.Fatalf("metrics.toolsUsed = %#v", metrics.toolsUsed)
	}
	if metrics.estimatedTokens <= 0 {
		t.Fatalf("metrics.estimatedTokens = %d, want > 0", metrics.estimatedTokens)
	}
}
