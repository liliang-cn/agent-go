package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// optsRecordingLLM answers immediately and remembers the generation options
// the runtime handed it, which is where a knob either arrives or is lost.
type optsRecordingLLM struct {
	mu   sync.Mutex
	seen []domain.PromptCacheMode
}

func (l *optsRecordingLLM) record(opts *domain.GenerationOptions) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if opts == nil {
		l.seen = append(l.seen, domain.PromptCacheOff)
		return
	}
	l.seen = append(l.seen, opts.PromptCache)
}

func (l *optsRecordingLLM) modes() []domain.PromptCacheMode {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domain.PromptCacheMode(nil), l.seen...)
}

func (l *optsRecordingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *optsRecordingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *optsRecordingLLM) GenerateWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.record(opts)
	return &domain.GenerationResult{Content: "Done.", FinishReason: "stop"}, nil
}

func (l *optsRecordingLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	l.record(opts)
	return cb(&domain.GenerationResult{Content: "Done.", FinishReason: "stop"})
}

func (l *optsRecordingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *optsRecordingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func runRecordingPromptCache(t *testing.T, name string, enabled bool) []domain.PromptCacheMode {
	t.Helper()
	llm := &optsRecordingLLM{}
	b := New(name).WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm)
	if enabled {
		b = b.WithPromptCache(true)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Say something.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}
	return llm.modes()
}

// The knob is useless unless every tool round carries it: one unmarked round
// pays full prefill and leaves the next one cold.
func TestWithPromptCacheReachesEveryTurn(t *testing.T) {
	modes := runRecordingPromptCache(t, "prompt-cache-on", true)
	if len(modes) == 0 {
		t.Fatal("the model was never called")
	}
	for i, mode := range modes {
		if mode != domain.PromptCacheExplicit {
			t.Errorf("turn %d sent PromptCache=%q, want %q", i, mode, domain.PromptCacheExplicit)
		}
	}
}

// usageReportingLLM answers with provider-reported token accounting, the way
// a real endpoint does.
type usageReportingLLM struct{ usage domain.TokenUsage }

func (l *usageReportingLLM) result() *domain.GenerationResult {
	u := l.usage
	return &domain.GenerationResult{Content: "Done.", FinishReason: "stop", Usage: &u}
}

func (l *usageReportingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *usageReportingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *usageReportingLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.result(), nil
}

func (l *usageReportingLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.result())
}

func (l *usageReportingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *usageReportingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// Turning caching on is only worth anything if the caller can see what it
// did, so the provider's own numbers have to survive the trip out of the loop.
func TestRunResultCarriesProviderUsage(t *testing.T) {
	llm := &usageReportingLLM{usage: domain.TokenUsage{
		PromptTokens:       1200,
		CompletionTokens:   40,
		CachedPromptTokens: 1024,
		CacheWriteTokens:   150,
	}}
	svc, err := New("usage-passthrough").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	result, err := svc.Run(context.Background(), "Say something.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Usage == nil {
		t.Fatal("the provider reported usage and the result dropped it")
	}
	if result.Usage.CachedPromptTokens != 1024 {
		t.Errorf("CachedPromptTokens = %d, want 1024", result.Usage.CachedPromptTokens)
	}
	if result.Usage.CacheWriteTokens != 150 {
		t.Errorf("CacheWriteTokens = %d, want 150", result.Usage.CacheWriteTokens)
	}
	if result.Usage.PromptTokens != 1200 {
		t.Errorf("PromptTokens = %d, want 1200", result.Usage.PromptTokens)
	}
}

// A provider that reports nothing must leave Usage nil rather than zeroed:
// "not measured" and "measured zero" are different answers, and only the
// first one means "go look at your provider".
func TestRunResultUsageIsNilWhenNothingWasReported(t *testing.T) {
	svc, err := New("usage-absent").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&optsRecordingLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	result, err := svc.Run(context.Background(), "Say something.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Usage != nil {
		t.Errorf("expected nil Usage, got %+v", result.Usage)
	}
}

// Off by default: whether an endpoint honours a marker is a fact about that
// endpoint, so nothing is sent until someone says to send it.
func TestPromptCacheIsOffByDefault(t *testing.T) {
	modes := runRecordingPromptCache(t, "prompt-cache-default", false)
	if len(modes) == 0 {
		t.Fatal("the model was never called")
	}
	for i, mode := range modes {
		if mode != domain.PromptCacheOff {
			t.Errorf("turn %d sent PromptCache=%q, want it unset", i, mode)
		}
	}
}
