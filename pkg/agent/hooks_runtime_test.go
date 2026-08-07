package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// TestHookData_UserPromptSubmitMutation pins the contract that handlers for
// HookEventUserPromptSubmit can both rewrite UserPrompt and append
// AdditionalSystemMessages, and that the registry returns the merged data.
func TestHookData_UserPromptSubmitMutation(t *testing.T) {
	t.Parallel()

	reg := NewHookRegistry()
	reg.Register(HookEventUserPromptSubmit, func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		data.UserPrompt = "rewritten: " + data.UserPrompt
		data.AdditionalSystemMessages = append(data.AdditionalSystemMessages,
			domain.Message{Role: "system", Content: "injected context"})
		return data, nil
	})

	out, err := reg.EmitWithResult(context.Background(), HookEventUserPromptSubmit, HookData{
		UserPrompt: "original goal",
	})
	if err != nil {
		t.Fatalf("EmitWithResult error: %v", err)
	}
	if out.UserPrompt != "rewritten: original goal" {
		t.Fatalf("UserPrompt = %q", out.UserPrompt)
	}
	if len(out.AdditionalSystemMessages) != 1 || out.AdditionalSystemMessages[0].Content != "injected context" {
		t.Fatalf("AdditionalSystemMessages = %+v", out.AdditionalSystemMessages)
	}
}

// captureStreamLLM records every messages slice it receives and replies with
// scripted GenerationResults. It satisfies enough of the LLM interface for
// the runtime's streaming loop.
type captureStreamLLM struct {
	mu       sync.Mutex
	replies  []*domain.GenerationResult
	calls    int32
	captured [][]domain.Message
}

func (l *captureStreamLLM) next() *domain.GenerationResult {
	idx := int(atomic.AddInt32(&l.calls, 1)) - 1
	if idx >= len(l.replies) {
		idx = len(l.replies) - 1
	}
	if idx < 0 {
		return &domain.GenerationResult{}
	}
	r := *l.replies[idx]
	r.ToolCalls = append([]domain.ToolCall(nil), l.replies[idx].ToolCalls...)
	return &r
}

func (l *captureStreamLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *captureStreamLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	return nil
}
func (l *captureStreamLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.record(messages)
	return l.next(), nil
}
func (l *captureStreamLLM) StreamWithTools(ctx context.Context, messages []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.record(messages)
	return cb(l.next())
}
func (l *captureStreamLLM) GenerateStructured(ctx context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *captureStreamLLM) RecognizeIntent(ctx context.Context, _ string) (*domain.IntentResult, error) {
	return nil, nil
}

func (l *captureStreamLLM) record(msgs []domain.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cloned := append([]domain.Message(nil), msgs...)
	l.captured = append(l.captured, cloned)
}

func (l *captureStreamLLM) firstRound() []domain.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.captured) == 0 {
		return nil
	}
	return l.captured[0]
}

// TestRuntime_UserPromptSubmit_RewritesPromptAndInjectsContext exercises the
// full streaming path: a hook rewrites the goal and prepends a system message,
// and we assert the LLM sees both changes on the first round.
func TestRuntime_UserPromptSubmit_RewritesPromptAndInjectsContext(t *testing.T) {
	llm := &captureStreamLLM{
		replies: []*domain.GenerationResult{
			{Content: "done"},
		},
	}

	svc, err := New("user-prompt-submit-agent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	svc.RegisterHook(HookEventUserPromptSubmit, func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		data.UserPrompt = "rewritten goal"
		data.AdditionalSystemMessages = append(data.AdditionalSystemMessages,
			domain.Message{Role: "system", Content: "AGENT-GO TEST INJECTED CONTEXT"})
		return data, nil
	})

	events, err := svc.RunStream(context.Background(), "original goal")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	final, blocked, _ := collectStreamContent(t, events)
	if blocked != "" {
		t.Fatalf("expected completion, got blocked=%q", blocked)
	}
	if final != "done" {
		t.Fatalf("final = %q, want %q", final, "done")
	}

	first := llm.firstRound()
	if len(first) == 0 {
		t.Fatal("LLM was never called")
	}

	var foundInjection, foundRewritten bool
	for _, m := range first {
		if m.Role == "system" && strings.Contains(m.Content, "AGENT-GO TEST INJECTED CONTEXT") {
			foundInjection = true
		}
		if m.Role == "user" && strings.Contains(m.Content, "rewritten goal") {
			foundRewritten = true
		}
	}
	if !foundInjection {
		t.Errorf("expected injected system message in first-round messages; got %+v", first)
	}
	if !foundRewritten {
		t.Errorf("expected rewritten goal in first-round messages; got %+v", first)
	}
}
