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

func TestRuntimeEmitToolCallAndResult_BlockingToolEmitsStateUpdates(t *testing.T) {
	t.Parallel()

	svc := &Service{
		agent:           NewAgent("Assistant"),
		toolRegistry:    NewToolRegistry(),
		inProgressTools: make(map[string]int),
	}
	svc.toolRegistry.RegisterWithMetadata(
		domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:       "blocking_tool",
				Parameters: map[string]interface{}{"type": "object"},
			},
		},
		nil,
		CategoryCustom,
		ToolMetadata{InterruptBehavior: InterruptBehaviorBlock},
	)

	runtime := &Runtime{
		svc:          svc,
		currentAgent: svc.agent,
		eventChan:    make(chan *Event, 8),
	}

	behavior, endExecution := svc.beginToolExecution("blocking_tool", svc.agent)
	runtime.emitToolCall("blocking_tool", map[string]interface{}{"x": 1}, behavior)
	endExecution()
	runtime.emitToolResult("blocking_tool", "ok", nil, behavior)

	toolCall := <-runtime.eventChan
	startState := <-runtime.eventChan
	toolResult := <-runtime.eventChan
	endState := <-runtime.eventChan

	if toolCall.Type != EventTypeToolCall {
		t.Fatalf("first event = %+v, want tool_call", toolCall)
	}
	if startState.Type != EventTypeStateUpdate || startState.StateDelta["interruptible"] != false {
		t.Fatalf("start state event = %+v, want non-interruptible state update", startState)
	}
	if got := startState.StateDelta["blocking_tool_count"]; got != 1 {
		t.Fatalf("expected blocking_tool_count=1, got %#v", got)
	}
	if toolResult.Type != EventTypeToolResult {
		t.Fatalf("third event = %+v, want tool_result", toolResult)
	}
	if endState.Type != EventTypeStateUpdate || endState.StateDelta["interruptible"] != true {
		t.Fatalf("end state event = %+v, want interruptible state update", endState)
	}
	if got := endState.StateDelta["blocking_tool_count"]; got != 0 {
		t.Fatalf("expected blocking_tool_count=0, got %#v", got)
	}
}

func TestRuntimeBlockingToolStateUpdate_TracksRemainingCount(t *testing.T) {
	t.Parallel()

	svc := &Service{
		agent:           NewAgent("Assistant"),
		toolRegistry:    NewToolRegistry(),
		inProgressTools: make(map[string]int),
	}
	runtime := &Runtime{
		svc:          svc,
		currentAgent: svc.agent,
		eventChan:    make(chan *Event, 8),
	}

	_, end1 := svc.beginToolExecution("memory_save", nil)
	behavior2, end2 := svc.beginToolExecution("delegate_to_subagent", nil)
	runtime.emitToolCall("delegate_to_subagent", nil, behavior2)
	end2()
	runtime.emitToolResult("delegate_to_subagent", "ok", nil, behavior2)
	end1()

	_ = <-runtime.eventChan
	startState := <-runtime.eventChan
	_ = <-runtime.eventChan
	endState := <-runtime.eventChan

	if got := startState.StateDelta["blocking_tool_count"]; got != 2 {
		t.Fatalf("expected start blocking count 2, got %#v", got)
	}
	if got := endState.StateDelta["blocking_tool_count"]; got != 1 {
		t.Fatalf("expected end blocking count 1, got %#v", got)
	}
	if endState.StateDelta["interruptible"] != false {
		t.Fatalf("expected still non-interruptible, got %+v", endState)
	}
}

func TestRuntimeEmitTurnState_IncludesStageAndReason(t *testing.T) {
	t.Parallel()

	svc := &Service{
		agent:           NewAgent("Assistant"),
		inProgressTools: make(map[string]int),
	}
	runtime := &Runtime{
		svc:          svc,
		currentAgent: svc.agent,
		eventChan:    make(chan *Event, 2),
	}

	runtime.emitTurnState(TurnStageAwaitingModel, "requesting model output", 2, 3, &IntentRecognitionResult{
		IntentType:     "web_search",
		PreferredAgent: defaultOperatorAgentName,
		RequiresTools:  true,
		Transition:     "tool_first",
	})
	evt := <-runtime.eventChan
	if evt.Type != EventTypeStateUpdate {
		t.Fatalf("event = %+v, want state update", evt)
	}
	if got := evt.StateDelta["turn_stage"]; got != TurnStageAwaitingModel {
		t.Fatalf("turn_stage = %#v, want %q", got, TurnStageAwaitingModel)
	}
	if got := evt.StateDelta["transition_reason"]; got != "requesting model output" {
		t.Fatalf("transition_reason = %#v", got)
	}
	if got := evt.StateDelta["round"]; got != 2 {
		t.Fatalf("round = %#v, want 2", got)
	}
	if got := evt.StateDelta["tool_call_count"]; got != 3 {
		t.Fatalf("tool_call_count = %#v, want 3", got)
	}
	if got := evt.StateDelta["intent_type"]; got != "web_search" {
		t.Fatalf("intent_type = %#v, want web_search", got)
	}
}

func TestRuntimeEmitToolState_IncludesQueuedExecutingCompleted(t *testing.T) {
	t.Parallel()

	svc := &Service{
		agent:           NewAgent("Assistant"),
		inProgressTools: make(map[string]int),
	}
	runtime := &Runtime{
		svc:          svc,
		currentAgent: svc.agent,
		eventChan:    make(chan *Event, 4),
	}

	runtime.emitToolState("echo_tool", "queued", InterruptBehaviorCancel)
	runtime.emitToolState("echo_tool", "executing", InterruptBehaviorCancel)
	runtime.emitToolState("echo_tool", "completed", InterruptBehaviorCancel)

	for _, want := range []string{"queued", "executing", "completed"} {
		evt := <-runtime.eventChan
		if evt.Type != EventTypeStateUpdate {
			t.Fatalf("event = %+v, want state update", evt)
		}
		if got := evt.StateDelta["tool_state"]; got != want {
			t.Fatalf("tool_state = %#v, want %q", got, want)
		}
	}
}
