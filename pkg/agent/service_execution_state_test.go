package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type serviceExecutionStateTestLLM struct {
	results       []*domain.GenerationResult
	generateCalls int
	seenMessages  [][]domain.Message
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
	clonedMessages := append([]domain.Message(nil), messages...)
	l.seenMessages = append(l.seenMessages, clonedMessages)
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
	result, err := l.GenerateWithTools(ctx, messages, tools, opts)
	if err != nil {
		return err
	}
	return callback(result)
}

func (l *serviceExecutionStateTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{
		Valid: true,
		Raw:   `{"intent_type":"analysis","confidence":0.9}`,
	}, nil
}

func (l *serviceExecutionStateTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestRunPersistsNonStreamingTaskEventsAndFrames(t *testing.T) {
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
			{Content: "done"},
		},
	}

	agent := NewAgent("Responder")
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

	svc, err := NewService(llm, nil, nil, filepath.Join(t.TempDir(), "agent.db"), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc.agent = agent
	svc.llmService = llm
	svc.registry.Register(agent)

	result, err := svc.Run(context.Background(), "inspect repo")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.TaskID == "" {
		t.Fatal("expected task id from Run")
	}

	// Asserted immediately, not polled: every task write now happens under the
	// task lock and completes before Run returns, so anything missing here is a
	// real regression rather than a slow machine.
	task, err := svc.store.GetTask(result.TaskID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if task == nil {
		t.Fatal("task never appeared in the store")
	}
	if len(task.Events) == 0 {
		t.Fatalf("expected persisted task events, got %+v", task)
	}
	var hasToolCall, hasToolResult bool
	for _, evt := range task.Events {
		if evt.Type == string(EventTypeToolCall) {
			hasToolCall = true
		}
		if evt.Type == string(EventTypeToolResult) {
			hasToolResult = true
		}
	}
	if !hasToolCall || !hasToolResult {
		t.Fatalf("expected tool call/result events, got %+v", task.Events)
	}
	if len(task.Frames) < 2 {
		t.Fatalf("expected persisted task frames, got %+v", task.Frames)
	}
}
