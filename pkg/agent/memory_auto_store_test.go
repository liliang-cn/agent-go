package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// autoStoreTestLLM answers the memory-extraction call with a fixed "yes" and
// records whether it was asked at all.
type autoStoreTestLLM struct {
	mu     sync.Mutex
	called int
}

func (l *autoStoreTestLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *autoStoreTestLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *autoStoreTestLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}

func (l *autoStoreTestLLM) StreamWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions, domain.ToolCallCallback) error {
	return nil
}

func (l *autoStoreTestLLM) GenerateStructured(_ context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	l.mu.Lock()
	l.called++
	l.mu.Unlock()
	return &domain.StructuredResult{
		Raw:   `{"should_store": true, "memories": [{"type": "fact", "content": "the deploy key lives in 1Password", "importance": 0.9}]}`,
		Valid: true,
	}, nil
}

func (l *autoStoreTestLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{}, nil
}

func (l *autoStoreTestLLM) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.called
}

// A question-shaped goal used to be dropped before the extraction call ever
// ran — the "how " prefix matched the information-seeking table. The fact it
// carried went unremembered. Auto-store is unconditional now.
func TestMemoryAutoStoreIsOnByDefault(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())
	ctx := context.Background()

	store := newMinimalStore()
	llm := &autoStoreTestLLM{}

	b := New("remembering-agent").WithConfig(cfg).WithMemory(WithMemoryStore(store))
	memSvc, _, err := b.buildMemoryService(cfg, nil, llm)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}

	err = memSvc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID:  "s1",
		AgentID:    "remembering-agent",
		TaskGoal:   "How do I rotate the deploy key? Ours lives in 1Password.",
		TaskResult: "Rotate it from the 1Password vault, then redeploy.",
	})
	if err != nil {
		t.Fatalf("StoreIfWorthwhile() error = %v", err)
	}

	if llm.calls() == 0 {
		t.Fatal("extraction call never ran; auto-store must not be gated on the wording of the goal")
	}
	mems, err := store.SearchByText(ctx, "1Password", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("nothing was written; the extracted memory must reach the store")
	}
}

func TestWithMemoryAutoStoreFalseSkipsWrites(t *testing.T) {
	cfg := testAgentConfig(t.TempDir())
	ctx := context.Background()

	store := newMinimalStore()
	llm := &autoStoreTestLLM{}

	b := New("forgetful-agent").WithConfig(cfg).WithMemory(
		WithMemoryStore(store),
		WithMemoryAutoStore(false),
	)
	memSvc, _, err := b.buildMemoryService(cfg, nil, llm)
	if err != nil {
		t.Fatalf("buildMemoryService() error = %v", err)
	}

	err = memSvc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID:  "s1",
		AgentID:    "forgetful-agent",
		TaskGoal:   "How do I rotate the deploy key? Ours lives in 1Password.",
		TaskResult: "Rotate it from the 1Password vault, then redeploy.",
	})
	if err != nil {
		t.Fatalf("StoreIfWorthwhile() error = %v", err)
	}

	if llm.calls() != 0 {
		t.Fatalf("extraction call ran %d times despite WithMemoryAutoStore(false)", llm.calls())
	}
	mems, err := store.SearchByText(ctx, "1Password", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("wrote %d memories despite WithMemoryAutoStore(false)", len(mems))
	}
}
