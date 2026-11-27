package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recallingServerStore is a store that embeds and recalls on its own side,
// as a shared brain does.
type recallingServerStore struct {
	serverEmbeddingStore
	result *domain.GraphRecallResult
	calls  int
}

func (s *recallingServerStore) KnowledgeRecall(context.Context, string, int, *domain.MemoryScope) (*domain.GraphRecallResult, error) {
	s.calls++
	return s.result, nil
}

// Injection on such a store uses its recall, so the brain's knowledge and
// what its graph walk found reach the prompt, not only a memory search.
func TestRetrieveAndInjectUsesTheBrainsRecall(t *testing.T) {
	fs, err := store.NewFileMemoryStore(t.TempDir())
	require.NoError(t, err)
	st := &recallingServerStore{serverEmbeddingStore: serverEmbeddingStore{fs}, result: &domain.GraphRecallResult{
		Memories: []*domain.MemoryWithScore{{Memory: &domain.Memory{ID: "m1", Type: domain.MemoryTypeFact,
			ScopeType: domain.MemoryScopeGlobal, Content: "zzbrain Omar took over Beacon"}, Score: 0.2}},
		Knowledge: []domain.GraphKnowledgeHit{{ID: "beacon", Title: "Beacon", Snippet: "zzbrain Beacon sends Ember's push", Score: 9}},
	}}
	cfg := DefaultConfig()
	cfg.ReflectThreshold = 0
	svc := NewService(st, nil, nil, cfg)
	t.Cleanup(func() { _ = svc.Close() })

	text, mems, err := svc.RetrieveAndInject(context.Background(), "who runs mobile push", "")
	require.NoError(t, err)
	assert.Equal(t, 1, st.calls)
	require.Len(t, mems, 2)
	assert.Equal(t, "m1", mems[0].Memory.ID, "the lists interleave by rank; a score on another scale does not reorder them")
	assert.Equal(t, knowledgeHitIDPrefix+"beacon", mems[1].Memory.ID)
	assert.True(t, strings.Contains(text, "Omar took over Beacon"), text)
	assert.True(t, strings.Contains(text, "Beacon sends Ember's push"), text)
}
