package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func TestFileMemoryIntegration(t *testing.T) {
	ctx := context.Background()

	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	service := NewService(memStore, nil, nil, nil)

	mem := &domain.Memory{
		ID:         "pref-1",
		Content:    "Alice likes tea in the morning.",
		Type:       domain.MemoryTypePreference,
		Importance: 0.9,
		SessionID:  "session-file-1",
	}
	if err := service.Add(ctx, mem); err != nil {
		t.Fatalf("add failed: %v", err)
	}

	mems, total, err := service.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if total != 1 || len(mems) != 1 {
		t.Fatalf("unexpected list result: total=%d len=%d", total, len(mems))
	}

	results, err := service.Search(ctx, "tea", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Content != mem.Content {
		t.Fatalf("unexpected search results: %+v", results)
	}

	formatted, recalled, err := service.RetrieveAndInject(ctx, "what does Alice like to drink?", "session-file-1")
	if err != nil {
		t.Fatalf("retrieve and inject failed: %v", err)
	}
	if formatted == "" || len(recalled) == 0 {
		t.Fatalf("expected retrieved memory context, got formatted=%q recalled=%d", formatted, len(recalled))
	}
	if !strings.Contains(formatted, "## Memory Index") {
		t.Fatalf("expected formatted context to include memory index, got %q", formatted)
	}
	if !strings.Contains(formatted, "# MEMORY") {
		t.Fatalf("expected formatted context to include MEMORY entrypoint, got %q", formatted)
	}
	if recalled[0].Content != mem.Content {
		t.Fatalf("unexpected recalled memory: %+v", recalled[0])
	}

	if err := service.Delete(ctx, mem.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	mems, total, err = service.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list after delete failed: %v", err)
	}
	if total != 0 || len(mems) != 0 {
		t.Fatalf("expected empty list after delete, got total=%d len=%d", total, len(mems))
	}
}

type scopedNavigatorTestLLM struct {
	lastNavigatorPrompt string
}

type backgroundMemoryTestLLM struct{}

func (b *backgroundMemoryTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (b *backgroundMemoryTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (b *backgroundMemoryTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: ""}, nil
}

func (b *backgroundMemoryTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (b *backgroundMemoryTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{
		Raw:   `{"should_store":true,"memories":[{"type":"fact","content":"Alice prefers green tea","importance":0.9,"scope":"session"}]}`,
		Valid: true,
	}, nil
}

func (b *backgroundMemoryTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func (s *scopedNavigatorTestLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (s *scopedNavigatorTestLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, callback func(string)) error {
	return nil
}

func (s *scopedNavigatorTestLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: "OK"}, nil
}

func (s *scopedNavigatorTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	return nil
}

func (s *scopedNavigatorTestLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	s.lastNavigatorPrompt = prompt
	return &domain.StructuredResult{
		Raw:   `{"ids":["team-alpha-memory"],"reasoning":"Selected the team-scoped memory from the higher-priority scope."}`,
		Valid: true,
	}, nil
}

func (s *scopedNavigatorTestLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestFileMemoryIntegrationScopedNavigatorRetrieval(t *testing.T) {
	ctx := context.Background()

	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	llm := &scopedNavigatorTestLLM{}
	service := NewService(memStore, llm, nil, nil)

	globalMem := &domain.Memory{
		ID:         "global-memory",
		Type:       domain.MemoryTypeFact,
		Content:    "Global fallback memory",
		Importance: 0.3,
	}
	teamMem := &domain.Memory{
		ID:         "team-alpha-memory",
		Type:       domain.MemoryTypeObservation,
		Content:    "Team alpha has already approved the deployment checklist.",
		Importance: 0.9,
	}
	if err := memStore.Store(ctx, globalMem); err != nil {
		t.Fatalf("store global memory failed: %v", err)
	}
	if err := memStore.StoreWithScope(ctx, teamMem, domain.MemoryScope{Type: domain.MemoryScopeTeam, ID: "alpha"}); err != nil {
		t.Fatalf("store team memory failed: %v", err)
	}
	if err := memStore.RebuildIndex(ctx); err != nil {
		t.Fatalf("rebuild index failed: %v", err)
	}

	formatted, recalled, reasoning, err := service.RetrieveAndInjectWithContextAndLogic(ctx, "what is the deployment status for team alpha?", domain.MemoryQueryContext{
		TeamID: "alpha",
	})
	if err != nil {
		t.Fatalf("scoped retrieve and inject failed: %v", err)
	}
	if len(recalled) != 1 || recalled[0].ID != "team-alpha-memory" {
		t.Fatalf("expected team-scoped memory, got %+v", recalled)
	}
	if reasoning == "" {
		t.Fatal("expected navigator reasoning to be returned")
	}
	if formatted == "" {
		t.Fatal("expected formatted memory context")
	}
	if llm.lastNavigatorPrompt == "" || !containsAll(llm.lastNavigatorPrompt, "## Scope: team:alpha", "## Scope: global", "team-alpha-memory") {
		t.Fatalf("expected navigator prompt to include scoped indexes, got:\n%s", llm.lastNavigatorPrompt)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func TestFileMemoryIntegrationBackgroundDurableWorker(t *testing.T) {
	ctx := context.Background()

	memStore, err := store.NewFileMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file memory store failed: %v", err)
	}

	service := NewService(memStore, &backgroundMemoryTestLLM{}, nil, nil)
	ok := service.EnqueueStoreIfWorthwhile(&domain.MemoryStoreRequest{
		SessionID:  "session-bg-1",
		TaskGoal:   "Remember Alice's tea preference",
		TaskResult: "Alice prefers green tea",
	})
	if !ok {
		t.Fatal("expected durable worker enqueue to succeed")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mems, total, err := service.List(ctx, 10, 0)
		if err == nil && total > 0 && len(mems) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected background worker to store memories, got total=%d err=%v", total, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
