package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The store recalls through the brain's knowledge_memory_recall and maps its
// memories, its knowledge and the walk's entities to domain types.
func TestCortexRemoteKnowledgeRecall(t *testing.T) {
	s, fake := newTestRemoteStore(t)
	fake.recallJSON = `{
		"memories": [{"memory": {"id": "m1", "scope": "global", "session_id": "memory:global:agentgo-test",
			"content": "Omar took over Beacon", "importance": 0.7}, "score": 0.9}],
		"knowledge": [{"knowledge_id": "beacon", "title": "Beacon", "snippet": "Beacon sends push", "score": 0.5, "entities": ["Beacon"]}],
		"entities": ["Ember", "Beacon"],
		"graph_entities": ["Beacon", "Omar"],
		"context_pack": {"text": "ctx"}
	}`
	var _ domain.KnowledgeGraphRecaller = s

	res, err := s.KnowledgeRecall(context.Background(), "who runs mobile push", 5, nil)
	require.NoError(t, err)
	require.Len(t, res.Memories, 1)
	assert.Equal(t, "m1", res.Memories[0].Memory.ID)
	assert.Equal(t, "Omar took over Beacon", res.Memories[0].Memory.Content)
	assert.Equal(t, 0.9, res.Memories[0].Score)
	require.Len(t, res.Knowledge, 1)
	assert.Equal(t, "beacon", res.Knowledge[0].ID)
	assert.Equal(t, []string{"Beacon", "Omar", "Ember"}, res.Entities, "the walk's entities lead, each once")
	assert.Equal(t, "ctx", res.ContextText)

	require.Len(t, fake.recallCalls, 1)
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(fake.recallCalls[0]), &args))
	assert.Equal(t, "who runs mobile push", args["query"])
	assert.Equal(t, "agentgo-test", args["namespace"])
	assert.Equal(t, "global", args["scope"])
	assert.EqualValues(t, 5, args["top_k_memories"])
	assert.EqualValues(t, 5, args["top_k_knowledge"])
}
