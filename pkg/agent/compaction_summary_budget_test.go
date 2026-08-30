package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// reasoningSummarizerLLM answers Generate only when given enough room: below
// minTokens the whole budget goes to reasoning and the visible answer is empty,
// which is what a reasoning model does on a real compaction transcript.
type reasoningSummarizerLLM struct {
	minTokens int

	mu      sync.Mutex
	budgets []int
}

func (l *reasoningSummarizerLLM) seen() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.budgets...)
}

func (l *reasoningSummarizerLLM) Generate(_ context.Context, _ string, opts *domain.GenerationOptions) (string, error) {
	max := 0
	if opts != nil {
		max = opts.MaxTokens
	}
	l.mu.Lock()
	l.budgets = append(l.budgets, max)
	l.mu.Unlock()
	if max < l.minTokens {
		return "", nil
	}
	return "- the agent read files and wrote handlers", nil
}
func (l *reasoningSummarizerLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *reasoningSummarizerLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: "ok", FinishReason: "stop"}, nil
}
func (l *reasoningSummarizerLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{Content: "ok", FinishReason: "stop"})
}
func (l *reasoningSummarizerLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *reasoningSummarizerLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func longHistory() []domain.Message {
	msgs := []domain.Message{{Role: "system", Content: "sys"}}
	for range 12 {
		msgs = append(msgs,
			domain.Message{Role: "assistant", Content: "reading"},
			domain.Message{Role: "tool", Content: strings.Repeat("file contents ", 200)})
	}
	return msgs
}

// A summary that came back empty used to fail compaction outright, so the
// runtime kept the unfolded history and the context grew without bound. On a
// real run that failed nineteen times in thirty-four rounds, and nothing said
// so: the error had no observer.
func TestCompactionSummaryEscalatesItsBudget(t *testing.T) {
	llm := &reasoningSummarizerLLM{minTokens: compactionSummaryMaxTokens * 2}
	svc, err := New("compaction-budget").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	msgs := longHistory()
	out, err := svc.compactMessages(context.Background(), msgs, 6)
	if err != nil {
		t.Fatalf("compaction failed even with escalation: %v", err)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("history not folded: %d -> %d", len(msgs), len(out))
	}

	budgets := llm.seen()
	if len(budgets) < 2 {
		t.Fatalf("expected the summary to be retried with more room, saw %v", budgets)
	}
	if budgets[0] != compactionSummaryMaxTokens {
		t.Errorf("first attempt used %d, want %d", budgets[0], compactionSummaryMaxTokens)
	}
	if budgets[1] <= budgets[0] {
		t.Errorf("budget did not grow: %v", budgets)
	}
}

// It must give up rather than escalate forever.
func TestCompactionSummaryEscalationIsBounded(t *testing.T) {
	llm := &reasoningSummarizerLLM{minTokens: 1 << 30}
	svc, err := New("compaction-budget-bounded").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.compactMessages(context.Background(), longHistory(), 6); err == nil {
		t.Fatal("expected compaction to give up when no budget suffices")
	}
	if n := len(llm.seen()); n > compactionSummaryEscalations+1 {
		t.Fatalf("tried %d times, bounded at %d", n, compactionSummaryEscalations+1)
	}
}
