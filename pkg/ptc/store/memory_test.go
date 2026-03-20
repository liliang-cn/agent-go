package store

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

func TestMemoryStore_SaveAndGet(t *testing.T) {
	store := NewMemoryStore(100)

	history := &ptc.ExecutionHistory{
		ID: "test-1",
		Request: &ptc.ExecutionRequest{
			Code:     "1 + 1",
			Language: ptc.LanguageJavaScript,
		},
		Result: &ptc.ExecutionResult{
			ID:       "test-1",
			Success:  true,
			Duration: time.Millisecond,
		},
		ExecutedAt: time.Now(),
	}

	ctx := context.Background()
	if err := store.Save(ctx, history); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	retrieved, err := store.Get(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}

	if retrieved == nil {
		t.Fatal("expected history, got nil")
	}

	if retrieved.ID != "test-1" {
		t.Errorf("expected ID 'test-1', got %s", retrieved.ID)
	}
}

func TestMemoryStore_List(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	// Save multiple entries
	for i := 0; i < 5; i++ {
		history := &ptc.ExecutionHistory{
			ID: string(rune('a' + i)),
			Request: &ptc.ExecutionRequest{
				Code:     "1 + 1",
				Language: ptc.LanguageJavaScript,
			},
			Result: &ptc.ExecutionResult{
				ID:       string(rune('a' + i)),
				Success:  true,
				Duration: time.Millisecond,
			},
			ExecutedAt: time.Now(),
		}
		if err := store.Save(ctx, history); err != nil {
			t.Fatalf("failed to save: %v", err)
		}
	}

	// List all
	list, err := store.List(ctx, 10)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}

	if len(list) != 5 {
		t.Errorf("expected 5 entries, got %d", len(list))
	}

	// List with limit
	list, err = store.List(ctx, 3)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}

	if len(list) != 3 {
		t.Errorf("expected 3 entries, got %d", len(list))
	}
}

func TestMemoryStore_MaxSize(t *testing.T) {
	store := NewMemoryStore(5)
	ctx := context.Background()

	// Save more entries than max size
	for i := 0; i < 10; i++ {
		history := &ptc.ExecutionHistory{
			ID: string(rune('0' + i)),
			Request: &ptc.ExecutionRequest{
				Code:     "1 + 1",
				Language: ptc.LanguageJavaScript,
			},
			Result: &ptc.ExecutionResult{
				ID:       string(rune('0' + i)),
				Success:  true,
				Duration: time.Millisecond,
			},
			ExecutedAt: time.Now(),
		}
		if err := store.Save(ctx, history); err != nil {
			t.Fatalf("failed to save: %v", err)
		}
	}

	// Should only have 5 entries
	if store.Size() != 5 {
		t.Errorf("expected 5 entries, got %d", store.Size())
	}
}

func TestMemoryStore_Delete(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	// Save entries
	now := time.Now()
	old := now.Add(-2 * time.Hour)

	history1 := &ptc.ExecutionHistory{
		ID:         "old",
		Request:    &ptc.ExecutionRequest{Code: "1"},
		Result:     &ptc.ExecutionResult{ID: "old", Success: true},
		ExecutedAt: old,
	}
	history2 := &ptc.ExecutionHistory{
		ID:         "new",
		Request:    &ptc.ExecutionRequest{Code: "2"},
		Result:     &ptc.ExecutionResult{ID: "new", Success: true},
		ExecutedAt: now,
	}

	store.Save(ctx, history1)
	store.Save(ctx, history2)

	// Delete old entries
	if err := store.Delete(ctx, now.Add(-time.Hour)); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Should only have new entry
	if store.Size() != 1 {
		t.Errorf("expected 1 entry, got %d", store.Size())
	}

	// Old entry should be gone
	retrieved, _ := store.Get(ctx, "old")
	if retrieved != nil {
		t.Error("expected old entry to be deleted")
	}

	// New entry should still exist
	retrieved, _ = store.Get(ctx, "new")
	if retrieved == nil {
		t.Error("expected new entry to exist")
	}
}

func TestMemoryStore_Clear(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	// Save entries
	for i := 0; i < 5; i++ {
		history := &ptc.ExecutionHistory{
			ID:         string(rune('a' + i)),
			Request:    &ptc.ExecutionRequest{Code: "1"},
			Result:     &ptc.ExecutionResult{ID: string(rune('a' + i)), Success: true},
			ExecutedAt: time.Now(),
		}
		store.Save(ctx, history)
	}

	// Clear
	if err := store.Clear(); err != nil {
		t.Fatalf("failed to clear: %v", err)
	}

	if store.Size() != 0 {
		t.Errorf("expected 0 entries after clear, got %d", store.Size())
	}
}
