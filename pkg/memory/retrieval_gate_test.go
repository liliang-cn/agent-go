package memory

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Retrieval must never be skipped because of how the request happens to be
// worded. The deleted QueryClassifier decided "list all my memories" was a
// command and returned needsMemory=false — the one query where memory is the
// entire point of the turn. These cases pin that the store is asked every time.
func TestRetrievalIsUnconditional(t *testing.T) {
	ctx := context.Background()

	queries := []struct {
		name  string
		query string
	}{
		{"explicit memory listing", "list all my memories"},
		{"show my preferences", "show me my preferences"},
		{"imperative command", "run the deployment script"},
		{"greeting", "hello"},
		{"acknowledgement", "thanks"},
		{"chinese question", "我上次说的偏好是什么"},
		{"chinese command", "列出我所有的记忆"},
		{"very short", "hi"},
	}

	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			store := new(MockMemoryStore)
			store.On("SearchByText", ctx, tc.query, mock.AnythingOfType("int")).
				Return([]*domain.MemoryWithScore{}, nil)
			store.On("List", ctx, mock.AnythingOfType("int"), 0).
				Return([]*domain.Memory{}, 0, nil)

			svc := NewService(store, nil, nil, DefaultConfig())

			_, _, err := svc.RetrieveAndInject(ctx, tc.query, "session-1")

			assert.NoError(t, err)
			store.AssertCalled(t, "SearchByText", ctx, tc.query, mock.AnythingOfType("int"))
		})
	}
}

// The only supported way to skip retrieval is an explicit operator setting.
func TestDisableRetrievalSkipsTheStore(t *testing.T) {
	ctx := context.Background()

	store := new(MockMemoryStore)
	cfg := DefaultConfig()
	cfg.DisableRetrieval = true
	svc := NewService(store, nil, nil, cfg)

	formatted, memories, logic, err := svc.RetrieveAndInjectWithContextAndLogic(
		ctx, "what did I say about the release?", domain.MemoryQueryContext{SessionID: "session-1"})

	assert.NoError(t, err)
	assert.Empty(t, formatted)
	assert.Nil(t, memories)
	assert.Empty(t, logic)
	store.AssertNotCalled(t, "SearchByText")
	store.AssertNotCalled(t, "SearchByScope")
	store.AssertNotCalled(t, "List")
}

// Retrieval is on unless the embedder asks for it to be off.
func TestDefaultConfigKeepsRetrievalOn(t *testing.T) {
	assert.False(t, DefaultConfig().DisableRetrieval)
}
