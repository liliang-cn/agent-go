package memory

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryIntegration(t *testing.T) {
	ctx := context.Background()
	dbPath := "test_memory.db"
	defer os.Remove(dbPath)

	memStore, err := store.NewMemoryStore(dbPath)
	require.NoError(t, err)
	defer memStore.Close()

	// Use nil for LLM and Embedder for basic store/list test
	service := NewService(memStore, nil, nil, nil)

	// 1. Add a memory
	mem := &domain.Memory{
		Content:    "The secret ingredient is love.",
		Type:       domain.MemoryTypeFact,
		Importance: 0.9,
	}
	err = service.Add(ctx, mem)
	assert.NoError(t, err)

	// 2. List memories
	mems, total, err := service.List(ctx, 10, 0)
	assert.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, mems, 1)
	assert.Equal(t, "The secret ingredient is love.", mems[0].Content)

	// 3. Search (should fallback to List since embedder is nil)
	results, err := service.Search(ctx, "secret", 5)
	assert.NoError(t, err)
	assert.NotEmpty(t, results)
	assert.Equal(t, "The secret ingredient is love.", results[0].Content)

	// 4. Delete
	err = service.Delete(ctx, mems[0].ID)
	assert.NoError(t, err)

	// 5. Verify deletion
	mems, total, err = service.List(ctx, 10, 0)
	assert.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, mems)
}

type integrationKeywordEmbedder struct{}

func (integrationKeywordEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "tea"), strings.Contains(lower, "drink"):
		return []float64{1, 0, 0}, nil
	case strings.Contains(lower, "concise"), strings.Contains(lower, "answer"):
		return []float64{0, 1, 0}, nil
	default:
		return []float64{0, 0, 1}, nil
	}
}

func (e integrationKeywordEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	results := make([][]float64, 0, len(texts))
	for _, text := range texts {
		vector, err := e.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results = append(results, vector)
	}
	return results, nil
}

func TestMemoryIntegrationVectorRetrievalUsesCortexDBMemoryBuckets(t *testing.T) {
	ctx := context.Background()
	dbPath := "test_memory_vector.db"
	defer os.Remove(dbPath)

	memStore, err := store.NewMemoryStore(dbPath)
	require.NoError(t, err)
	defer memStore.Close()

	service := NewService(memStore, nil, integrationKeywordEmbedder{}, nil)

	err = service.Add(ctx, &domain.Memory{
		ID:         "session-tea",
		SessionID:  "session-123",
		Type:       domain.MemoryTypePreference,
		Content:    "Alice likes tea in the morning.",
		Importance: 0.9,
	})
	require.NoError(t, err)

	err = service.Add(ctx, &domain.Memory{
		ID:         "agent-style",
		ScopeType:  domain.MemoryScopeAgent,
		ScopeID:    "assistant-main",
		Type:       domain.MemoryTypePattern,
		Content:    "Answer concisely with factual bullet points.",
		Importance: 0.8,
	})
	require.NoError(t, err)

	err = service.Add(ctx, &domain.Memory{
		ID:         "global-fact",
		Type:       domain.MemoryTypeFact,
		Content:    "Go is a compiled programming language.",
		Importance: 0.3,
	})
	require.NoError(t, err)

	searchResults, err := service.Search(ctx, "What should Alice drink?", 3)
	require.NoError(t, err)
	require.NotEmpty(t, searchResults)
	assert.Equal(t, "session-tea", searchResults[0].ID)

	formatted, recalled, err := service.RetrieveAndInjectWithContext(ctx, "How should I answer the user?", domain.MemoryQueryContext{
		SessionID: "session-123",
		AgentID:   "assistant-main",
	})
	require.NoError(t, err)
	require.NotEmpty(t, recalled)
	assert.Equal(t, "agent-style", recalled[0].ID)
	assert.Contains(t, formatted, "Answer concisely with factual bullet points.")
}
