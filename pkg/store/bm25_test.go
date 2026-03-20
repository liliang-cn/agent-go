package store

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/stretchr/testify/assert"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "Hello, world!",
			expected: []string{"hello", "world"},
		},
		{
			input:    "AgentGo is a modular RAG system.",
			expected: []string{"agentgo", "is", "modular", "rag", "system"},
		},
		{
			input:    "Go (Golang) is great; system-level programming!",
			expected: []string{"go", "golang", "is", "great", "system", "level", "programming"},
		},
		{
			input:    "A B C D", // Should skip single characters if the logic says > 1
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := tokenize(tt.input)
			if len(tt.expected) == 0 {
				assert.Empty(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestCalculateBM25(t *testing.T) {
	searcher := NewBM25Searcher("mock.db", nil)

	// Mock corpus stats
	docFreq := map[string]int{
		"golang":  1,
		"system":  2,
		"agentgo": 1,
	}
	totalDocs := 3
	avgDocLength := 5.0

	t.Run("High relevance match", func(t *testing.T) {
		docContent := "AgentGo is a golang system"
		queryTerms := []string{"agentgo", "golang"}

		score := searcher.calculateBM25(docContent, queryTerms, docFreq, totalDocs, avgDocLength)
		assert.True(t, score > 0)
	})

	t.Run("No match", func(t *testing.T) {
		docContent := "Python is another language"
		queryTerms := []string{"agentgo"}

		score := searcher.calculateBM25(docContent, queryTerms, docFreq, totalDocs, avgDocLength)
		assert.Equal(t, 0.0, score)
	})

	t.Run("IDF effect - rare terms score higher", func(t *testing.T) {
		// "agentgo" appears in 1/3 docs, "system" appears in 2/3 docs.
		// Matching "agentgo" should typically yield a higher score contribution than "system".

		doc1 := "agentgo is here"
		doc2 := "system is here"

		scoreAgentGo := searcher.calculateBM25(doc1, []string{"agentgo"}, docFreq, totalDocs, avgDocLength)
		scoreSystem := searcher.calculateBM25(doc2, []string{"system"}, docFreq, totalDocs, avgDocLength)

		assert.True(t, scoreAgentGo > scoreSystem, "Rare term 'agentgo' should have higher score than common term 'system'")
	})
}

func TestRRFFusion(t *testing.T) {
	// Mock search results
	vectorResults := []*domain.MemoryWithScore{
		{Memory: &domain.Memory{ID: "mem-A", Content: "A"}, Score: 0.9},
		{Memory: &domain.Memory{ID: "mem-B", Content: "B"}, Score: 0.8},
	}

	bm25Results := []*domain.MemoryWithScore{
		{Memory: &domain.Memory{ID: "mem-B", Content: "B"}, Score: 15.0},
		{Memory: &domain.Memory{ID: "mem-C", Content: "C"}, Score: 10.0},
	}

	t.Run("Fusion and Re-ranking", func(t *testing.T) {
		// In vector: A(rank 1), B(rank 2)
		// In BM25: B(rank 1), C(rank 2)
		// RRF Score for B: 1/(60+2) + 1/(60+1) -> approx 0.0161 + 0.0163 = 0.0324
		// RRF Score for A: 1/(60+1) -> approx 0.0163
		// RRF Score for C: 1/(60+2) -> approx 0.0161
		// Expected Order: B, A, C

		fused := RRFFusion(vectorResults, bm25Results, 60.0)

		assert.Len(t, fused, 3)
		assert.Equal(t, "mem-B", fused[0].ID)
		assert.Equal(t, "mem-A", fused[1].ID)
		assert.Equal(t, "mem-C", fused[2].ID)
	})
}
