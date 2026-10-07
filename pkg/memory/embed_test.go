package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingEmbedder counts calls and fails while down is set.
type countingEmbedder struct {
	calls int64
	down  atomic.Bool
}

func (e *countingEmbedder) Embed(context.Context, string) ([]float64, error) {
	atomic.AddInt64(&e.calls, 1)
	if e.down.Load() {
		return nil, errors.New("503 no node currently serves the model")
	}
	return []float64{0.1, 0.2, 0.3}, nil
}

func (e *countingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// serverEmbeddingStore is a file store that says it embeds for itself.
type serverEmbeddingStore struct{ *store.FileMemoryStore }

func (serverEmbeddingStore) EmbedsServerSide() bool { return true }

// A store that embeds on its own side gets no client-side vector: computing
// one cost a network call per retrieval and per write, and was thrown away.
func TestNoClientEmbeddingForAServerSideStore(t *testing.T) {
	ctx := context.Background()
	fs, err := store.NewFileMemoryStore(t.TempDir())
	require.NoError(t, err)
	emb := &countingEmbedder{}
	cfg := DefaultConfig()
	cfg.ReflectThreshold = 0
	svc := NewService(serverEmbeddingStore{fs}, nil, emb, cfg)
	t.Cleanup(func() { _ = svc.Close() })

	require.NoError(t, svc.Add(ctx, &domain.Memory{ID: "88888888-8888-4888-8888-888888888888", Type: domain.MemoryTypeFact,
		ScopeType: domain.MemoryScopeGlobal, Content: "zzserver Keke has piano on Fridays", CreatedAt: time.Now()}))
	_, _, err = svc.RetrieveAndInject(ctx, "zzserver piano", "")
	require.NoError(t, err)
	assert.Zero(t, atomic.LoadInt64(&emb.calls))
}

// A failing embedder is tried once, then skipped while it backs off, and
// tried again once the backoff has passed.
func TestFailingEmbedderIsSkippedWhileItBacksOff(t *testing.T) {
	ctx := context.Background()
	emb := &countingEmbedder{}
	emb.down.Store(true)
	svc := &Service{embedder: emb}

	for i := 0; i < 5; i++ {
		_, ok := svc.embed(ctx, "q")
		assert.False(t, ok)
	}
	assert.EqualValues(t, 1, atomic.LoadInt64(&emb.calls), "a down embedder must not be called on every request")

	emb.down.Store(false)
	svc.embedState.mu.Lock()
	svc.embedState.until = time.Now().Add(-time.Second) // the backoff has passed
	svc.embedState.mu.Unlock()
	vec, ok := svc.embed(ctx, "q")
	assert.True(t, ok)
	assert.Len(t, vec, 3)
	assert.Zero(t, svc.embedState.delay, "a success resets the backoff")
}

// unconfiguredEmbedder is a pool with no embedding provider in it yet.
type unconfiguredEmbedder struct {
	countingEmbedder
	configured atomic.Bool
}

func (e *unconfiguredEmbedder) EmbeddingConfigured() bool { return e.configured.Load() }

// No embedding model is not a failing one: it is not called, does not trip
// the backoff, and is used as soon as a provider is added.
func TestUnconfiguredEmbedderIsNotAFailure(t *testing.T) {
	ctx := context.Background()
	emb := &unconfiguredEmbedder{}
	svc := &Service{embedder: emb}

	_, ok := svc.embed(ctx, "q")
	assert.False(t, ok)
	assert.Zero(t, atomic.LoadInt64(&emb.calls))
	assert.Zero(t, svc.embedState.delay, "an absent embedder must not start a backoff")

	emb.configured.Store(true)
	_, ok = svc.embed(ctx, "q")
	assert.True(t, ok)
}
