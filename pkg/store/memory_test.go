package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestNewMemoryStoreConcurrentOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	const workers = 6
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			store, err := NewMemoryStore(dbPath)
			if err != nil {
				errCh <- err
				return
			}
			defer store.Close()

			if err := store.InitSchema(context.Background()); err != nil {
				errCh <- err
				return
			}

			if _, _, err := store.List(context.Background(), 10, 0); err != nil {
				errCh <- err
				return
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent memory store open failed: %v", err)
		}
	}
}

func TestMemoryStoreSearchByText_CJK(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	store, err := NewMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	defer store.Close()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	now := time.Now().UTC()
	memories := []*domain.Memory{
		{
			ID:         "m1",
			Type:       domain.MemoryTypeFact,
			Content:    "项目使用RAG系统做语义检索",
			Importance: 0.5,
			CreatedAt:  now,
		},
		{
			ID:         "m2",
			Type:       domain.MemoryTypeFact,
			Content:    "今天只讨论了系统设计，不涉及检索",
			Importance: 0.5,
			CreatedAt:  now,
		},
	}

	for _, mem := range memories {
		if err := store.Store(ctx, mem); err != nil {
			t.Fatalf("Store(%s) error = %v", mem.ID, err)
		}
	}

	results, err := store.SearchByText(ctx, "RAG系统", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchByText() returned no results")
	}
	if got := results[0].ID; got != "m1" {
		t.Fatalf("top result = %s, want m1", got)
	}
}

func TestMemoryStoreSearchByTextRanksByImportanceAndRecency(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	store, err := NewMemoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	defer store.Close()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	now := time.Now().UTC()
	memories := []*domain.Memory{
		{
			ID:         "high-importance",
			Type:       domain.MemoryTypeFact,
			Content:    "本地知识库使用 SQLite 存储",
			Importance: 0.9,
			CreatedAt:  now,
		},
		{
			ID:         "low-importance",
			Type:       domain.MemoryTypeFact,
			Content:    "本地知识库使用 Markdown 存储",
			Importance: 0.1,
			CreatedAt:  now,
		},
		{
			ID:         "very-old",
			Type:       domain.MemoryTypeFact,
			Content:    "本地知识库使用文档存储",
			Importance: 0.9,
			CreatedAt:  now.Add(-365 * 24 * time.Hour),
		},
	}

	for _, mem := range memories {
		if err := store.Store(ctx, mem); err != nil {
			t.Fatalf("Store(%s) error = %v", mem.ID, err)
		}
	}

	results, err := store.SearchByText(ctx, "本地知识库", 10)
	if err != nil {
		t.Fatalf("SearchByText() error = %v", err)
	}
	if len(results) < 3 {
		t.Fatalf("SearchByText() returned %d results, want at least 3", len(results))
	}
	if got := results[0].ID; got != "high-importance" {
		t.Fatalf("top result = %s, want high-importance", got)
	}

	positions := make(map[string]int, len(results))
	for i, res := range results {
		positions[res.ID] = i
	}
	if positions["low-importance"] > positions["very-old"] {
		t.Fatalf("expected recent low-importance result to outrank very old result, got positions %+v", positions)
	}
}
