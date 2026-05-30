package processor

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestReciprocalRankFusion(t *testing.T) {
	// Vector search results
	vectorChunks := []domain.Chunk{
		{ID: "chunk1", Content: "Vector search match 1"},
		{ID: "chunk2", Content: "Vector search match 2"},
		{ID: "chunk3", Content: "Vector search match 3"},
	}

	// Keyword search results
	keywordChunks := []domain.Chunk{
		{ID: "chunk2", Content: "Keyword search match 2"}, // overlapping
		{ID: "chunk4", Content: "Keyword search match 4"}, // non-overlapping
		{ID: "chunk1", Content: "Keyword search match 1"}, // overlapping but different rank
	}

	// Perform RRF
	merged := ReciprocalRankFusion(vectorChunks, keywordChunks, 10)

	// Check counts: distinct IDs should be chunk1, chunk2, chunk3, chunk4
	if len(merged) != 4 {
		t.Fatalf("expected 4 merged chunks, got %d", len(merged))
	}

	// Verify order
	// chunk2: vector rank 2, keyword rank 1 -> 1/(60+2) + 1/(60+1) = 1/62 + 1/61 = 0.016129 + 0.016393 = 0.032522
	// chunk1: vector rank 1, keyword rank 3 -> 1/(60+1) + 1/(60+3) = 1/61 + 1/63 = 0.016393 + 0.015873 = 0.032266
	// chunk3: vector rank 3 -> 1/(60+3) = 1/63 = 0.015873
	// chunk4: keyword rank 2 -> 1/(60+2) = 1/62 = 0.016129
	//
	// Expected descending order: chunk2, chunk1, chunk4, chunk3
	expectedOrder := []string{"chunk2", "chunk1", "chunk4", "chunk3"}
	for i, expID := range expectedOrder {
		if merged[i].ID != expID {
			t.Errorf("expected chunk at index %d to be %s, got %s", i, expID, merged[i].ID)
		}
	}

	// Verify topK limit works
	limited := ReciprocalRankFusion(vectorChunks, keywordChunks, 2)
	if len(limited) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(limited))
	}
	if limited[0].ID != "chunk2" || limited[1].ID != "chunk1" {
		t.Errorf("expected limited chunks to be chunk2 and chunk1, got %v", limited)
	}
}
