package memory

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

const autoStoreExtraction = `{"should_store": true, "memories": [{"type": "fact", "content": "extracted", "importance": 0.8}]}`

// The auto-store pre-filter was the last input-side phrase table: a byte-length
// floor, a greeting list, twelve "explicit save" prefixes and thirty
// "information seeking" prefixes decided, from the wording of the request,
// whether the extraction call ran at all. Every one of these goals was thrown
// away before the model was asked — including ones that plainly carry a durable
// fact. The extraction call itself is the decision now.
func TestAutoStoreExtractionIsUnconditional(t *testing.T) {
	ctx := context.Background()

	goals := []struct {
		name string
		goal string
	}{
		{"how-do-I question carrying a fact", "How do I stop the build from failing? I am on Go 1.26 and my module is agent-go."},
		{"what question carrying a preference", "What editor should I use? I have always preferred Neovim."},
		{"chinese question carrying a fact", "我该怎么配置代理？我家里的 mihomo 在 192.168.123.98 上。"},
		{"multi-line goal", "Ship the release.\nRemember that the signing key lives in 1Password."},
		{"tell me prefix", "tell me my deploy target — it is the hp box, not the dell one"},
		{"greeting", "hi"},
		{"short chinese statement", "我叫李亮"},
		{"list prefix", "list the things I said I care about"},
	}

	for _, tc := range goals {
		t.Run(tc.name, func(t *testing.T) {
			store := new(MockMemoryStore)
			llm := new(MockGenerator)
			svc := NewService(store, llm, nil, DefaultConfig())

			llm.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).
				Return(&domain.StructuredResult{Raw: autoStoreExtraction, Valid: true}, nil)
			store.On("Store", ctx, mock.AnythingOfType("*domain.Memory")).Return(nil)

			err := svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
				SessionID:  "session-1",
				TaskGoal:   tc.goal,
				TaskResult: "Done.",
			})

			assert.NoError(t, err)
			llm.AssertCalled(t, "GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything)
			store.AssertCalled(t, "Store", ctx, mock.AnythingOfType("*domain.Memory"))
		})
	}
}

// The only pre-filter left is emptiness: nothing to extract, nothing to ask.
func TestAutoStoreSkipsAnEmptyInteraction(t *testing.T) {
	ctx := context.Background()

	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	svc := NewService(store, llm, nil, DefaultConfig())

	err := svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID:  "session-1",
		TaskGoal:   "   ",
		TaskResult: "",
	})

	assert.NoError(t, err)
	llm.AssertNotCalled(t, "GenerateStructured")
	store.AssertNotCalled(t, "Store")
}

// The only supported way to skip auto-store is an explicit operator setting.
func TestDisableAutoStoreSkipsTheExtractionCall(t *testing.T) {
	ctx := context.Background()

	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	cfg := DefaultConfig()
	cfg.DisableAutoStore = true
	svc := NewService(store, llm, nil, cfg)

	err := svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID:  "session-1",
		TaskGoal:   "Remember that the signing key lives in 1Password.",
		TaskResult: "Noted.",
	})

	assert.NoError(t, err)
	llm.AssertNotCalled(t, "GenerateStructured")
	store.AssertNotCalled(t, "Store")
}

// The background durable writer goes through the same gate.
func TestDisableAutoStoreAlsoSilencesTheDurableQueue(t *testing.T) {
	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	cfg := DefaultConfig()
	cfg.DisableAutoStore = true
	svc := NewService(store, llm, nil, cfg)

	// A non-file store has no durable worker, so the enqueue is refused and the
	// caller falls back to the synchronous path — which is also disabled.
	assert.False(t, svc.EnqueueStoreIfWorthwhile(&domain.MemoryStoreRequest{
		SessionID: "session-1",
		TaskGoal:  "Remember that the signing key lives in 1Password.",
	}))
	store.AssertNotCalled(t, "Store")
}

// Auto-store is on unless the embedder asks for it to be off.
func TestDefaultConfigKeepsAutoStoreOn(t *testing.T) {
	assert.False(t, DefaultConfig().DisableAutoStore)
}

// A failed or unparseable extraction degrades to storing nothing. It must never
// fall back to a keyword guess about what the user "probably" meant.
func TestFailedExtractionStoresNothing(t *testing.T) {
	ctx := context.Background()

	store := new(MockMemoryStore)
	llm := new(MockGenerator)
	svc := NewService(store, llm, nil, DefaultConfig())

	llm.On("GenerateStructured", ctx, mock.Anything, mock.Anything, mock.Anything).
		Return(&domain.StructuredResult{Raw: "", Valid: false}, assert.AnError)

	err := svc.StoreIfWorthwhile(ctx, &domain.MemoryStoreRequest{
		SessionID:  "session-1",
		TaskGoal:   "Alice prefers coffee over tea.",
		TaskResult: "Understood.",
	})

	assert.NoError(t, err)
	store.AssertNotCalled(t, "Store")
}
