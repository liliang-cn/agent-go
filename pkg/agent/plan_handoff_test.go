package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// promptCapturingLLM records the system prompt of every turn, which is where
// context either arrives or is silently missing.
type promptCapturingLLM struct {
	mu      sync.Mutex
	prompts []string
}

func (l *promptCapturingLLM) capture(messages []domain.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range messages {
		if m.Role == "system" {
			l.prompts = append(l.prompts, m.Content)
			return
		}
	}
	l.prompts = append(l.prompts, "")
}

func (l *promptCapturingLLM) systemPrompts() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.prompts...)
}

func (l *promptCapturingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *promptCapturingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *promptCapturingLLM) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.capture(messages)
	return &domain.GenerationResult{Content: "Done.", FinishReason: "stop"}, nil
}

func (l *promptCapturingLLM) StreamWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.capture(messages)
	return cb(&domain.GenerationResult{Content: "Done.", FinishReason: "stop"})
}

func (l *promptCapturingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *promptCapturingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// memoryPlanStore is a PlanStore that already holds a half-finished plan, the
// way one written by an earlier process would.
type memoryPlanStore struct {
	mu    sync.Mutex
	plans map[string][]PlanItem
}

func (m *memoryPlanStore) LoadPlan(_ context.Context, key string) ([]PlanItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plans[key], nil
}

func (m *memoryPlanStore) SavePlan(_ context.Context, key string, items []PlanItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plans == nil {
		m.plans = map[string][]PlanItem{}
	}
	m.plans[key] = items
	return nil
}

// The gap this closes: the plan survived the process and nobody told the model
// about it, so a resumed run started over holding the answer.
func TestResumedRunIsToldWhatTheEarlierOneFinished(t *testing.T) {
	llm := &promptCapturingLLM{}
	// Under the task's own key: a plan is the task's, and a run that has none
	// of its own is not handed another task's (see planSummaryForRun).
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		taskScopedPlanKey("task-resume"): {
			{Text: "find the gateway port", Done: true, Note: "port 47821, from settings.json"},
			{Text: "write the client", Done: true, Note: "client.go, uses grpc.NewClient"},
			{Text: "run the tests", Done: false},
		},
	}}

	svc, err := New("plan-handoff").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPlanStore(store).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// RunStream on purpose: it is the API a host drives a long run with, and
	// the one that used to skip every run-start injection.
	events, err := svc.RunStreamWithOptions(context.Background(), "Carry on.", WithTaskID("task-resume"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	prompts := llm.systemPrompts()
	if len(prompts) == 0 {
		t.Fatal("the model was never called")
	}
	first := prompts[0]
	for _, want := range []string{
		"Work already done on this task",
		"port 47821, from settings.json",
		"client.go, uses grpc.NewClient",
		"run the tests",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("the system prompt never mentioned %q", want)
		}
	}
}

// Nothing to hand over must add nothing at all — an empty section is prompt
// the model reads on every turn of every run for no reason.
func TestRunWithNoPlanGetsNoHandoffSection(t *testing.T) {
	llm := &promptCapturingLLM{}
	svc, err := New("plan-handoff-empty").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPlanStore(&memoryPlanStore{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Start fresh.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	for i, p := range llm.systemPrompts() {
		if strings.Contains(p, "Work already done on this task") {
			t.Errorf("turn %d carried a hand-off section with no plan to hand over", i)
		}
	}
}

// A plan nobody has started is not progress: the model is about to write one
// for itself, and telling it "0 of 3 done" is noise.
func TestUnstartedPlanIsNotHandedOver(t *testing.T) {
	llm := &promptCapturingLLM{}
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		scratchpadDefaultKey: {{Text: "do the thing", Done: false}},
	}}
	svc, err := New("plan-handoff-unstarted").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPlanStore(store).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Start.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	for i, p := range llm.systemPrompts() {
		if strings.Contains(p, "Work already done on this task") {
			t.Errorf("turn %d handed over a plan nothing had been done on", i)
		}
	}
}

// The hand-off is resolved once and must not drift between turns: it rides at
// the end of the system prompt, so a section that changed every round would
// invalidate the provider's cache of everything after it, every turn.
func TestPlanHandoffIsIdenticalOnEveryTurn(t *testing.T) {
	llm := &promptCapturingLLM{}
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		scratchpadDefaultKey: {
			{Text: "step one", Done: true, Note: "did it"},
			{Text: "step two", Done: false},
		},
	}}
	svc, err := New("plan-handoff-stable").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithPlanStore(store).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Carry on.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	prompts := llm.systemPrompts()
	for i := 1; i < len(prompts); i++ {
		if prompts[i] != prompts[0] {
			t.Errorf("turn %d's system prompt differs from turn 0's; the cache prefix breaks every round", i)
		}
	}
}
