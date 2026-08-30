package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// truncatingLLM stands in for a model that reasons before it writes: below
// minTokens the whole budget goes to reasoning the caller never sees, and the
// provider reports finish_reason="length" with nothing in it.
type truncatingLLM struct {
	minTokens int

	mu     sync.Mutex
	budget []int // the MaxTokens of every request, in order
}

func (l *truncatingLLM) seen() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.budget...)
}

func (l *truncatingLLM) reply(opts *domain.GenerationOptions) *domain.GenerationResult {
	maxTokens := 0
	if opts != nil {
		maxTokens = opts.MaxTokens
	}
	l.mu.Lock()
	l.budget = append(l.budget, maxTokens)
	l.mu.Unlock()

	if maxTokens < l.minTokens {
		return &domain.GenerationResult{Content: "", FinishReason: "length"}
	}
	return &domain.GenerationResult{Content: "All done: the work is finished.", FinishReason: "stop"}
}

func (l *truncatingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *truncatingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *truncatingLLM) GenerateWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(opts), nil
}
func (l *truncatingLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(opts))
}
func (l *truncatingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *truncatingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// A turn cut off before it produced anything must be asked again with a larger
// budget — not handed to the lints as an empty answer, which reads as the
// model refusing to speak and blocks the run.
func TestTruncatedTurnEscalatesInsteadOfBlocking(t *testing.T) {
	// Needs more than the 8192 default, less than one 4x step.
	llm := &truncatingLLM{minTokens: 20000}
	svc, err := New("truncation").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	res, err := svc.Run(context.Background(), "Say something.", WithConstraintExtraction(false))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Blocked {
		t.Fatalf("run blocked on a truncated turn; final=%q", res.Text())
	}
	if res.Text() == "" {
		t.Fatal("expected the escalated turn's text to survive")
	}

	seen := llm.seen()
	if len(seen) < 2 {
		t.Fatalf("expected the turn to be retried with a bigger budget, saw budgets %v", seen)
	}
	if seen[0] != defaultRunMaxTokens {
		t.Fatalf("first attempt used %d, want the default %d", seen[0], defaultRunMaxTokens)
	}
	if seen[1] <= seen[0] {
		t.Fatalf("budget did not grow: %v", seen)
	}
}

// Escalation is bounded: a model that produces nothing however much room it
// gets must fall through to the ordinary terminal paths, not loop forever.
func TestTruncationEscalationIsBounded(t *testing.T) {
	llm := &truncatingLLM{minTokens: 1 << 30} // never satisfied
	svc, err := New("truncation-bounded").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.Run(context.Background(), "Say something.", WithConstraintExtraction(false), WithMaxTurns(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// One round: the first attempt plus at most maxTokenEscalations retries.
	// Anything beyond that is an unbounded loop.
	grew := 0
	seen := llm.seen()
	for i := 1; i < len(seen); i++ {
		if seen[i] > seen[i-1] {
			grew++
		}
	}
	if grew > maxTokenEscalations {
		t.Fatalf("escalated %d times, bounded at %d: %v", grew, maxTokenEscalations, seen)
	}
}
