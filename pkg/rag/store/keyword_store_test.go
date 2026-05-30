package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestBleveStore(t *testing.T) {
	// Create a temp directory inside the project for testing
	tempDir, err := os.MkdirTemp("", "bleve-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	indexPath := filepath.Join(tempDir, "test.idx")
	store, err := NewBleveStore(indexPath)
	if err != nil {
		t.Fatalf("failed to create BleveStore: %v", err)
	}

	ctx := context.Background()

	// 1. Check Store operations
	chunks := []domain.Chunk{
		{
			ID:         "doc1_chunk0",
			DocumentID: "doc1",
			Content:    "Implementing Hybrid Search in Go using Bleve and Vector search",
			Metadata:   map[string]interface{}{"author": "Antigravity", "tags": []interface{}{"go", "rag"}},
		},
		{
			ID:         "doc1_chunk1",
			DocumentID: "doc1",
			Content:    "Reciprocal Rank Fusion is an awesome search merging algorithm",
			Metadata:   map[string]interface{}{"author": "Antigravity"},
		},
		{
			ID:         "doc2_chunk0",
			DocumentID: "doc2",
			Content:    "We love pair programming and writing beautiful Go code together",
			Metadata:   map[string]interface{}{"author": "User"},
		},
	}

	err = store.Store(ctx, chunks)
	if err != nil {
		t.Fatalf("failed to store chunks: %v", err)
	}

	// 2. Check Search operations
	t.Run("Search Keyword", func(t *testing.T) {
		results, err := store.Search(ctx, "hybrid", 5)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}

		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}

		if results[0].ID != "doc1_chunk0" {
			t.Errorf("expected doc1_chunk0, got %s", results[0].ID)
		}

		// Verify metadata deserialization works perfectly
		author, _ := results[0].Metadata["author"].(string)
		if author != "Antigravity" {
			t.Errorf("expected author Antigravity, got %v", results[0].Metadata["author"])
		}
	})

	t.Run("Search Multiple Matches", func(t *testing.T) {
		results, err := store.Search(ctx, "search", 5)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}

		if len(results) != 2 {
			t.Fatalf("expected 2 results for search keyword 'search', got %d", len(results))
		}
	})

	// 3. Check Delete operations
	t.Run("Delete Document", func(t *testing.T) {
		err = store.Delete(ctx, "doc1")
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}

		// Verify doc1 chunks are deleted
		results, err := store.Search(ctx, "fusion", 5)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for deleted document chunk search, got %d", len(results))
		}

		// Verify doc2 chunk is still there
		results, err = store.Search(ctx, "beautiful", 5)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected doc2_chunk0 to still exist, got %d results", len(results))
		}
	})

	// 4. Check Reset operations
	t.Run("Reset Store", func(t *testing.T) {
		err = store.Reset(ctx)
		if err != nil {
			t.Fatalf("reset failed: %v", err)
		}

		// Verify all chunks are deleted
		results, err := store.Search(ctx, "beautiful", 5)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results after reset, got %d", len(results))
		}
	})
}
