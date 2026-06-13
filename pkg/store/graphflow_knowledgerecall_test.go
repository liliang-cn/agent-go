package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Real graph path: store a fact, then KnowledgeRecall returns a populated,
// provider-agnostic GraphRecallResult (the agent-loop entry point).
func TestGraphFlowStoreKnowledgeRecall(t *testing.T) {
	ctx := context.Background()
	store, err := NewGraphFlowStore(filepath.Join(t.TempDir(), "kr.db"))
	if err != nil {
		t.Fatalf("NewGraphFlowStore() error = %v", err)
	}
	defer store.Close()

	// Satisfies the optional capability interface used by the agent loop.
	var _ domain.KnowledgeGraphRecaller = store

	mem := &domain.Memory{
		ID:         "kr-1",
		Type:       domain.MemoryTypeFact,
		Content:    "Alice owns Apollo and Apollo ships Friday.",
		Importance: 0.8,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	res, err := store.KnowledgeRecall(ctx, "what does Alice own?", 5, nil)
	if err != nil {
		t.Fatalf("KnowledgeRecall() error = %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil GraphRecallResult")
	}
	if res.Query != "what does Alice own?" {
		t.Fatalf("query = %q", res.Query)
	}
	// Graph extraction should surface at least one entity for this fact.
	if len(res.Entities) == 0 && len(res.Memories) == 0 {
		t.Fatal("expected entities or memories from graph recall")
	}
}
