package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// stubLLM is a minimal domain.Generator that records the system prompt it was
// handed and replies with fixed text.
type stubLLM struct {
	mu         sync.Mutex
	sysPrompts []string
}

func (s *stubLLM) record(messages []domain.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range messages {
		if m.Role == "system" {
			s.sysPrompts = append(s.sysPrompts, m.Content)
		}
	}
}

func (s *stubLLM) seenSystemPrompt(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.sysPrompts {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

func (s *stubLLM) Generate(_ context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	return "stub", nil
}

func (s *stubLLM) Stream(_ context.Context, _ string, _ *domain.GenerationOptions, cb func(string)) error {
	cb("stub")
	return nil
}

func (s *stubLLM) GenerateWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	s.record(messages)
	return &domain.GenerationResult{Content: "the stub answer", Finished: true, FinishReason: "stop"}, nil
}

func (s *stubLLM) StreamWithTools(_ context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	s.record(messages)
	return cb(&domain.GenerationResult{Content: "the stub answer", Finished: true, FinishReason: "stop"})
}

func (s *stubLLM) GenerateStructured(_ context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Raw: "{}", Valid: true}, nil
}

func (s *stubLLM) RecognizeIntent(_ context.Context, _ string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentQuestion}, nil
}

// fakeRunMemory records the recall goal and signals capture.
type fakeRunMemory struct {
	recallGoal string
	captured   chan string
}

func (f *fakeRunMemory) RecallForRun(_ context.Context, goal string) (string, error) {
	f.recallGoal = goal
	return "PRIOR-DECISION: db_pool_size was raised to 25.", nil
}

func (f *fakeRunMemory) CaptureRun(_ context.Context, _, finalText string) error {
	f.captured <- finalText
	return nil
}

// A run with a RunMemory attached must (1) inject the recalled context into
// the system prompt and (2) capture the successful run's final text.
func TestRunMemoryRecallInjectionAndCapture(t *testing.T) {
	t.Setenv("AGENTGO_HOME", t.TempDir())

	llm := &stubLLM{}
	rm := &fakeRunMemory{captured: make(chan string, 1)}

	svc, err := New("run-memory-test").
		WithLLM(llm).
		WithRunMemory(rm).
		WithDBPath(filepath.Join(t.TempDir(), "agent.db")).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(), "what did we decide about the pool?",
		WithToolsDisabled(), WithMaxTurns(1))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatalf("run not successful: %+v", res)
	}

	if rm.recallGoal == "" {
		t.Fatal("RecallForRun was never called")
	}
	if !llm.seenSystemPrompt("Recalled context (run memory)") ||
		!llm.seenSystemPrompt("PRIOR-DECISION") {
		t.Fatal("recalled context was not injected into the system prompt")
	}

	select {
	case got := <-rm.captured:
		if !strings.Contains(got, "the stub answer") {
			t.Fatalf("captured text = %q, want the run's final text", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CaptureRun was not called after a successful run")
	}
}

// A failing recall must not fail the run — memory is best-effort.
type failingRunMemory struct{}

func (failingRunMemory) RecallForRun(context.Context, string) (string, error) {
	return "", context.DeadlineExceeded
}
func (failingRunMemory) CaptureRun(context.Context, string, string) error { return nil }

func TestRunMemoryRecallFailureDoesNotBlockRun(t *testing.T) {
	t.Setenv("AGENTGO_HOME", t.TempDir())

	svc, err := New("run-memory-fail-test").
		WithLLM(&stubLLM{}).
		WithRunMemory(failingRunMemory{}).
		WithDBPath(filepath.Join(t.TempDir(), "agent.db")).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(), "hello",
		WithToolsDisabled(), WithMaxTurns(1))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatalf("run must succeed despite recall failure: %+v", res)
	}
}
