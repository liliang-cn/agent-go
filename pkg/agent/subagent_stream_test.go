package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type subAgentStreamTestLLM struct {
	round int
}

func (s *subAgentStreamTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (s *subAgentStreamTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (s *subAgentStreamTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: "done"}, nil
}

func (s *subAgentStreamTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	if s.round == 0 {
		s.round++
		if err := callback(&domain.GenerationResult{Content: "working "}); err != nil {
			return err
		}
		return callback(&domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:   "tc1",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "echo_tool",
					Arguments: map[string]interface{}{"text": "hello"},
				},
			}},
		})
	}
	return callback(&domain.GenerationResult{Content: "done"})
}

func (s *subAgentStreamTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true}, nil
}

func (s *subAgentStreamTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestSubAgentRunAsyncStreamsNestedEvents(t *testing.T) {
	t.Parallel()

	svc, err := NewService(&subAgentStreamTestLLM{}, nil, nil, filepath.Join(t.TempDir(), "agent.db"), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	svc.RegisterTool(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "echo_tool",
			Description: "Echo a string.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{"type": "string"},
				},
				"required": []string{"text"},
			},
		},
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		text, _ := args["text"].(string)
		return "echo:" + text, nil
	})

	subAgent := svc.CreateSubAgent(svc.agent, "Use the echo tool and answer.")
	events := subAgent.RunAsync(context.Background())

	seen := map[EventType]bool{}
	var partial strings.Builder
	for evt := range events {
		seen[evt.Type] = true
		if evt.Type == EventTypePartial {
			partial.WriteString(evt.Content)
		}
	}

	result, err := subAgent.GetResult()
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	if got := toolResultToString(result); got != "done" {
		t.Fatalf("result = %q, want %q", got, "done")
	}

	for _, evtType := range []EventType{EventTypeStart, EventTypeStateUpdate, EventTypePartial, EventTypeToolCall, EventTypeToolResult, EventTypeComplete} {
		if !seen[evtType] {
			t.Fatalf("expected event type %s to be emitted", evtType)
		}
	}
	if got := partial.String(); !strings.Contains(got, "working") || !strings.Contains(got, "done") {
		t.Fatalf("partial stream = %q, want both streaming chunks", got)
	}
}

func TestRuntimeForwardSubAgentEventRewritesTerminalEvents(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{
		currentAgent: NewAgent("Assistant"),
		eventChan:    make(chan *Event, 4),
	}

	runtime.forwardSubAgentEvent(&Event{Type: EventTypeComplete, AgentName: "Assistant", Content: "done"})
	runtime.forwardSubAgentEvent(&Event{Type: EventTypeError, AgentName: "Assistant", Content: "boom"})

	complete := <-runtime.eventChan
	errEvt := <-runtime.eventChan

	if complete.Type != EventTypeStateUpdate || !strings.Contains(complete.Content, "completed") {
		t.Fatalf("complete event = %+v, want state update completion", complete)
	}
	if errEvt.Type != EventTypeStateUpdate || !strings.Contains(errEvt.Content, "failed") {
		t.Fatalf("error event = %+v, want state update failure", errEvt)
	}
}
