package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── buildStepPrompt ───────────────────────────────────────────────────────────

func TestBuildStepPromptEmpty_UsesUpstreamDirectly(t *testing.T) {
	step := PipelineStep{AgentName: "Analyst"}
	got := buildStepPrompt(step, "upstream result")
	if got != "upstream result" {
		t.Fatalf("buildStepPrompt = %q, want %q", got, "upstream result")
	}
}

func TestBuildStepPromptTemplate_ReplacesInput(t *testing.T) {
	step := PipelineStep{AgentName: "Summariser", Prompt: "Summarise this: {input}"}
	got := buildStepPrompt(step, "raw text")
	want := "Summarise this: raw text"
	if got != want {
		t.Fatalf("buildStepPrompt = %q, want %q", got, want)
	}
}

func TestBuildStepPromptNoPlaceholder_UsesPromptVerbatim(t *testing.T) {
	step := PipelineStep{AgentName: "Analyst", Prompt: "fixed prompt"}
	got := buildStepPrompt(step, "ignored upstream")
	if got != "fixed prompt" {
		t.Fatalf("buildStepPrompt = %q, want %q", got, "fixed prompt")
	}
}

// ── RunPipeline validation ────────────────────────────────────────────────────

func TestRunPipelineEmptySteps_ReturnsError(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	manager := newIsolatedTeamRuntimeManager(t, store)

	_, err = manager.RunPipeline(context.Background(), nil, "initial")
	if err == nil {
		t.Fatal("expected error for empty steps")
	}
}

// ── Single-step pipeline ──────────────────────────────────────────────────────

func TestRunPipelineSingleStep_StreamsAndReturnsOutput(t *testing.T) {
	manager := newSeededPipelineManager(t)
	manager.builtInStreamDispatchOverride = fixedStreamDispatch(map[string][]fakeEvent{
		defaultResponderAgentName: {
			{typ: EventTypeStart, content: ""},
			{typ: EventTypePartial, content: "hello "},
			{typ: EventTypePartial, content: "world"},
			{typ: EventTypeComplete, content: "hello world"},
		},
	})

	events, err := manager.RunPipeline(context.Background(), []PipelineStep{
		{AgentName: defaultResponderAgentName},
	}, "do something")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	results, collectErr := CollectPipelineResult(events)
	if collectErr != nil {
		t.Fatalf("CollectPipelineResult: %v", collectErr)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 step result, got %d", len(results))
	}
	if results[0] != "hello world" {
		t.Fatalf("step 0 result = %q, want %q", results[0], "hello world")
	}
}

// ── Two-step sequential pipeline ─────────────────────────────────────────────

func TestRunPipelineTwoSteps_OutputOfFirstFeedsSecond(t *testing.T) {
	manager := newSeededPipelineManager(t)

	var (
		mu                  sync.Mutex
		step1ReceivedPrompt string
	)

	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		mu.Lock()
		defer mu.Unlock()
		ch := make(chan *Event, 4)
		switch agentName {
		case defaultResponderAgentName:
			ch <- &Event{Type: EventTypePartial, AgentName: agentName, Content: "step1 output"}
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "step1 output"}
		case defaultOperatorAgentName:
			step1ReceivedPrompt = instruction
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "step2 output"}
		}
		close(ch)
		return ch, nil
	}

	events, err := manager.RunPipeline(context.Background(), []PipelineStep{
		{AgentName: defaultResponderAgentName},
		{AgentName: defaultOperatorAgentName, Prompt: "process: {input}"},
	}, "initial")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	results, collectErr := CollectPipelineResult(events)
	if collectErr != nil {
		t.Fatalf("CollectPipelineResult: %v", collectErr)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 step results, got %d: %v", len(results), results)
	}
	if results[0] != "step1 output" {
		t.Fatalf("step 0 = %q, want %q", results[0], "step1 output")
	}
	if results[1] != "step2 output" {
		t.Fatalf("step 1 = %q, want %q", results[1], "step2 output")
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(step1ReceivedPrompt, "step1 output") {
		t.Fatalf("step 1 prompt should contain step 0 output, got %q", step1ReceivedPrompt)
	}
}

// ── Trigger mode ──────────────────────────────────────────────────────────────

func TestRunPipelineWithTriggerPattern_NextStepStartsEarly(t *testing.T) {
	manager := newSeededPipelineManager(t)

	step1Started := make(chan struct{})

	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		ch := make(chan *Event, 8)
		switch agentName {
		case defaultResponderAgentName:
			go func() {
				defer close(ch)
				// emit events slowly; trigger fires after READY appears
				ch <- &Event{Type: EventTypePartial, AgentName: agentName, Content: "preparing "}
				ch <- &Event{Type: EventTypePartial, AgentName: agentName, Content: "READY: data"}
				// simulate more output arriving after trigger
				time.Sleep(5 * time.Millisecond)
				ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "final step0"}
			}()
		case defaultOperatorAgentName:
			close(step1Started)
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "step1 result"}
			close(ch)
		}
		return ch, nil
	}

	events, err := manager.RunPipeline(context.Background(), []PipelineStep{
		{AgentName: defaultResponderAgentName, TriggerPattern: "READY"},
		{AgentName: defaultOperatorAgentName},
	}, "go")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	results, collectErr := CollectPipelineResult(events)
	if collectErr != nil {
		t.Fatalf("CollectPipelineResult: %v", collectErr)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 step results, got %d: %v", len(results), results)
	}
	if results[1] != "step1 result" {
		t.Fatalf("step 1 = %q, want %q", results[1], "step1 result")
	}

	select {
	case <-step1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("step 1 was never started")
	}
}

func TestRunPipelineWithTriggerPattern_NoTriggerHit_WaitsForCompletion(t *testing.T) {
	manager := newSeededPipelineManager(t)

	var step1Input string
	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		ch := make(chan *Event, 4)
		switch agentName {
		case defaultResponderAgentName:
			// pattern "SENTINEL" never appears
			ch <- &Event{Type: EventTypePartial, AgentName: agentName, Content: "no match here"}
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "full output"}
		case defaultOperatorAgentName:
			step1Input = instruction
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "ok"}
		}
		close(ch)
		return ch, nil
	}

	events, err := manager.RunPipeline(context.Background(), []PipelineStep{
		{AgentName: defaultResponderAgentName, TriggerPattern: "SENTINEL"},
		{AgentName: defaultOperatorAgentName},
	}, "start")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	_, collectErr := CollectPipelineResult(events)
	if collectErr != nil {
		t.Fatalf("CollectPipelineResult: %v", collectErr)
	}

	// When pattern never matches the complete event output is used as next input
	if !strings.Contains(step1Input, "full output") {
		t.Fatalf("step1 should receive step0's complete output, got %q", step1Input)
	}
}

// ── Error propagation ─────────────────────────────────────────────────────────

func TestRunPipelineStepError_StopsAndReturnsError(t *testing.T) {
	manager := newSeededPipelineManager(t)

	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		ch := make(chan *Event, 2)
		ch <- &Event{Type: EventTypeError, AgentName: agentName, Content: "agent exploded"}
		close(ch)
		return ch, nil
	}

	events, err := manager.RunPipeline(context.Background(), []PipelineStep{
		{AgentName: defaultResponderAgentName},
		{AgentName: defaultOperatorAgentName},
	}, "go")
	if err != nil {
		t.Fatalf("RunPipeline itself should not error: %v", err)
	}

	_, collectErr := CollectPipelineResult(events)
	if collectErr == nil {
		t.Fatal("expected error from failed step")
	}
	if !strings.Contains(collectErr.Error(), "agent exploded") {
		t.Fatalf("unexpected error: %v", collectErr)
	}
}

func TestRunPipelineDispatchError_StopsAndForwardsError(t *testing.T) {
	manager := newSeededPipelineManager(t)

	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		return nil, errors.New("dispatch failed")
	}

	events, err := manager.RunPipeline(context.Background(), []PipelineStep{
		{AgentName: defaultResponderAgentName},
	}, "go")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	_, collectErr := CollectPipelineResult(events)
	if collectErr == nil {
		t.Fatal("expected error from dispatch failure")
	}
}

// ── Context cancellation ──────────────────────────────────────────────────────

func TestRunPipelineContextCancellation_DrainsSafely(t *testing.T) {
	manager := newSeededPipelineManager(t)

	ctx, cancel := context.WithCancel(context.Background())

	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		ch := make(chan *Event, 1)
		go func() {
			defer close(ch)
			// cancel before sending anything
			cancel()
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}()
		return ch, nil
	}

	events, err := manager.RunPipeline(ctx, []PipelineStep{
		{AgentName: defaultResponderAgentName},
		{AgentName: defaultOperatorAgentName},
	}, "go")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	// Must drain without deadlock
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline channel did not close after context cancellation")
	}
}

// ── CollectPipelineResult ─────────────────────────────────────────────────────

func TestCollectPipelineResult_AccumulatesPartials(t *testing.T) {
	ch := make(chan *PipelineEvent, 8)
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypePartial, Content: "foo "}}
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypePartial, Content: "bar"}}
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypeComplete, Content: ""}}
	close(ch)

	results, err := CollectPipelineResult(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0] != "foo bar" {
		t.Fatalf("results = %v, want [foo bar]", results)
	}
}

func TestCollectPipelineResult_CompleteOverridesPartials(t *testing.T) {
	ch := make(chan *PipelineEvent, 4)
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypePartial, Content: "partial "}}
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypeComplete, Content: "authoritative"}}
	close(ch)

	results, err := CollectPipelineResult(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0] != "authoritative" {
		t.Fatalf("results[0] = %q, want %q", results[0], "authoritative")
	}
}

func TestCollectPipelineResult_MultiStepInterleaved(t *testing.T) {
	ch := make(chan *PipelineEvent, 8)
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypePartial, Content: "step0 "}}
	ch <- &PipelineEvent{StepIndex: 1, AgentName: "B", Event: &Event{Type: EventTypePartial, Content: "step1 "}}
	ch <- &PipelineEvent{StepIndex: 0, AgentName: "A", Event: &Event{Type: EventTypeComplete, Content: "step0 done"}}
	ch <- &PipelineEvent{StepIndex: 1, AgentName: "B", Event: &Event{Type: EventTypeComplete, Content: "step1 done"}}
	close(ch)

	results, err := CollectPipelineResult(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if results[0] != "step0 done" {
		t.Fatalf("results[0] = %q, want %q", results[0], "step0 done")
	}
	if results[1] != "step1 done" {
		t.Fatalf("results[1] = %q, want %q", results[1], "step1 done")
	}
}

func TestCollectPipelineResult_EmptyChannel(t *testing.T) {
	ch := make(chan *PipelineEvent)
	close(ch)

	results, err := CollectPipelineResult(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results, got %v", results)
	}
}

// ── delegate_pipeline tool ────────────────────────────────────────────────────

func TestDelegatePipelineTool_RegisteredOnOrchestrator(t *testing.T) {
	manager := newSeededPipelineManager(t)
	svc, err := manager.GetAgentService(BuiltInOrchestratorAgentName)
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	manager.RegisterOrchestratorTools(svc)

	if !svc.toolRegistry.Has("delegate_pipeline") {
		t.Fatal("expected delegate_pipeline to be registered on orchestrator")
	}
	meta := svc.toolRegistry.MetadataOf("delegate_pipeline")
	if meta.InterruptBehavior != InterruptBehaviorBlock {
		t.Fatalf("unexpected delegate_pipeline metadata: %+v", meta)
	}
}

func TestDelegatePipelineTool_RunsTwoStepsAndReturnsLastOutput(t *testing.T) {
	manager := newSeededPipelineManager(t)
	svc, err := manager.GetAgentService(BuiltInOrchestratorAgentName)
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	manager.RegisterOrchestratorTools(svc)

	var step1Instruction string
	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		ch := make(chan *Event, 4)
		switch agentName {
		case defaultResponderAgentName:
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "analysis complete"}
		case defaultOperatorAgentName:
			step1Instruction = instruction
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "action taken"}
		}
		close(ch)
		return ch, nil
	}

	raw, err := svc.toolRegistry.Call(context.Background(), "delegate_pipeline", map[string]interface{}{
		"initial_input": "audit the system",
		"steps": []interface{}{
			map[string]interface{}{"agent_name": defaultResponderAgentName},
			map[string]interface{}{
				"agent_name": defaultOperatorAgentName,
				"prompt":     "based on: {input} — take action",
			},
		},
	})
	if err != nil {
		t.Fatalf("delegate_pipeline: %v", err)
	}
	result, ok := raw.(string)
	if !ok {
		t.Fatalf("unexpected result type: %T", raw)
	}
	if result != "action taken" {
		t.Fatalf("result = %q, want %q", result, "action taken")
	}
	if !strings.Contains(step1Instruction, "analysis complete") {
		t.Fatalf("step 1 instruction should include step 0 output, got %q", step1Instruction)
	}
}

func TestDelegatePipelineTool_ForwardsEventsToSink(t *testing.T) {
	manager := newSeededPipelineManager(t)
	svc, err := manager.GetAgentService(BuiltInOrchestratorAgentName)
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	manager.RegisterOrchestratorTools(svc)

	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		ch := make(chan *Event, 4)
		ch <- &Event{Type: EventTypePartial, AgentName: agentName, Content: "chunk"}
		ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "done"}
		close(ch)
		return ch, nil
	}

	var forwarded []*Event
	ctx := withEventSink(context.Background(), func(evt *Event) {
		forwarded = append(forwarded, cloneAgentEvent(evt))
	})

	_, err = svc.toolRegistry.Call(ctx, "delegate_pipeline", map[string]interface{}{
		"initial_input": "go",
		"steps": []interface{}{
			map[string]interface{}{"agent_name": defaultResponderAgentName},
		},
	})
	if err != nil {
		t.Fatalf("delegate_pipeline: %v", err)
	}
	if len(forwarded) == 0 {
		t.Fatal("expected events to be forwarded to sink")
	}
}

func TestDelegatePipelineTool_EmptyStepsReturnsError(t *testing.T) {
	manager := newSeededPipelineManager(t)
	svc, err := manager.GetAgentService(BuiltInOrchestratorAgentName)
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	manager.RegisterOrchestratorTools(svc)

	_, err = svc.toolRegistry.Call(context.Background(), "delegate_pipeline", map[string]interface{}{
		"initial_input": "go",
		"steps":         []interface{}{},
	})
	if err == nil {
		t.Fatal("expected error for empty steps")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

type fakeEvent struct {
	typ     EventType
	content string
}

func fixedStreamDispatch(responses map[string][]fakeEvent) builtInRuntimeStreamDispatchFunc {
	return func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		evts, ok := responses[agentName]
		bufSize := len(evts)
		if bufSize < 1 {
			bufSize = 1
		}
		ch := make(chan *Event, bufSize)
		if ok {
			for _, fe := range evts {
				ch <- &Event{Type: fe.typ, AgentName: agentName, Content: fe.content}
			}
		} else {
			ch <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: ""}
		}
		close(ch)
		return ch, nil
	}
}

func newSeededPipelineManager(t *testing.T) *TeamManager {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	manager := newIsolatedTeamRuntimeManager(t, store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members: %v", err)
	}
	return manager
}
