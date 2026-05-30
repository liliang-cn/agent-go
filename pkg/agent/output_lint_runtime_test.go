package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// scriptedLintLLM is a minimal Generator stub that returns a different final
// answer per call, simulating how the runtime re-prompts the model after a
// lint rejection. It records how many times each surface (StreamWithTools /
// GenerateWithTools) was invoked so tests can assert the retry actually
// happened at the model layer.
type scriptedLintLLM struct {
	mu      sync.Mutex
	replies []string
	calls   int32
}

func (s *scriptedLintLLM) nextReply() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := int(atomic.AddInt32(&s.calls, 1)) - 1
	if idx >= len(s.replies) {
		idx = len(s.replies) - 1
	}
	if idx < 0 {
		return ""
	}
	return s.replies[idx]
}

func (s *scriptedLintLLM) callCount() int { return int(atomic.LoadInt32(&s.calls)) }

func (s *scriptedLintLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return s.nextReply(), nil
}

func (s *scriptedLintLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (s *scriptedLintLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: s.nextReply()}, nil
}

func (s *scriptedLintLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return callback(&domain.GenerationResult{Content: s.nextReply()})
}

func (s *scriptedLintLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return structuredJSON(map[string]interface{}{}), nil
}

func (s *scriptedLintLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

func collectStreamContent(t *testing.T, events <-chan *Event) (final string, blocked string, sawError bool) {
	t.Helper()
	for evt := range events {
		switch evt.Type {
		case EventTypeComplete:
			final = evt.Content
		case EventTypeBlocked:
			blocked = evt.Content
		case EventTypeError:
			sawError = true
		}
	}
	return
}

// rejectIfContains is a tiny deterministic lint used by the tests below.
type rejectIfContains struct {
	name    string
	needle  string
	reason  string
	calls   int32
	maxFail int32 // -1 means always fail
}

func (l *rejectIfContains) Name() string { return l.name }

func (l *rejectIfContains) Check(text string, ctx LintContext) (bool, string) {
	count := atomic.AddInt32(&l.calls, 1)
	if !strings.Contains(text, l.needle) {
		return true, ""
	}
	if l.maxFail < 0 || count <= l.maxFail {
		return false, l.reason
	}
	return true, ""
}

func TestRuntimeRetriesOnLintViolationAndCompletes(t *testing.T) {
	t.Parallel()

	llm := &scriptedLintLLM{
		replies: []string{
			"Routing this to Operator now.", // first try — trips the custom lint only
			"Routing complete.",             // retry — passes
		},
	}
	svc, err := New("lint-runtime-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	lint := &rejectIfContains{
		name:    "no_route_phrase",
		needle:  "Routing this",
		reason:  "response narrates routing instead of completing the task",
		maxFail: 1,
	}
	svc.RegisterOutputLint(lint)

	events, err := svc.RunStream(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	final, blocked, sawError := collectStreamContent(t, events)

	if blocked != "" {
		t.Fatalf("expected the run to complete after retry, got blocked=%q", blocked)
	}
	if final != "Routing complete." {
		t.Fatalf("expected retried final content, got %q", final)
	}
	if !sawError {
		t.Fatalf("expected a transient EventTypeError emitted on the first lint fail, but saw none")
	}
	if got := atomic.LoadInt32(&lint.calls); got != 2 {
		t.Fatalf("expected lint to run twice (first fail, then pass), got %d", got)
	}
	if got := llm.callCount(); got < 2 {
		t.Fatalf("expected LLM to be re-prompted at least once, got %d calls", got)
	}
}

func TestRuntimeBlocksWhenLintBudgetIsExhausted(t *testing.T) {
	t.Parallel()

	llm := &scriptedLintLLM{
		// Always returns a violating answer, even on retries. Provide enough
		// replies for budget+1 attempts.
		replies: []string{
			"Routing it to Operator.",
			"Routing it to Operator again.",
			"Routing it to Operator once more.",
			"Routing it to Operator forever.",
		},
	}
	svc, err := New("lint-runtime-block").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	lint := &rejectIfContains{
		name:    "no_route_phrase",
		needle:  "Routing it",
		reason:  "still routing instead of completing",
		maxFail: -1, // always fail
	}
	svc.RegisterOutputLint(lint)

	events, err := svc.RunStream(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	final, blocked, _ := collectStreamContent(t, events)

	if final != "" {
		t.Fatalf("expected no Complete event when lint never passes, got final=%q", final)
	}
	if blocked == "" {
		t.Fatalf("expected EventTypeBlocked when lint budget is exhausted")
	}
	if !strings.Contains(blocked, "no_route_phrase") {
		t.Fatalf("expected blocker to name the failing lint, got %q", blocked)
	}
	if got := atomic.LoadInt32(&lint.calls); got != int32(defaultLintRetryBudget+1) {
		t.Fatalf("expected lint to be invoked budget+1 times (=%d), got %d", defaultLintRetryBudget+1, got)
	}
}

func TestRuntimeIgnoresLintsWhenNoneRegistered(t *testing.T) {
	t.Parallel()

	llm := &scriptedLintLLM{replies: []string{"just done."}}
	svc, err := New("lint-runtime-noop").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "anything")
	if err != nil {
		t.Fatalf("RunStream failed: %v", err)
	}
	final, blocked, _ := collectStreamContent(t, events)
	if blocked != "" {
		t.Fatalf("expected no block when no lints registered, got %q", blocked)
	}
	if final != "just done." {
		t.Fatalf("expected first reply to pass straight through, got %q", final)
	}
	if got := llm.callCount(); got != 1 {
		t.Fatalf("expected exactly one LLM call without lints, got %d", got)
	}
}
