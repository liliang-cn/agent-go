package memory

import (
	"context"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// Embedding is a network call on every path that uses it — retrieval, the
// write-time reconciliation, every stored memory — so whether to make it at
// all is decided in one place.

const (
	embedBackoffStart = 30 * time.Second
	embedBackoffMax   = 10 * time.Minute
)

// embedBreaker remembers that the embedder just failed, so the next calls
// skip it for a while instead of each paying for the same failure.
type embedBreaker struct {
	mu    sync.Mutex
	until time.Time
	delay time.Duration
}

func (b *embedBreaker) open(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return now.Before(b.until)
}

// failed backs off, doubling; it reports whether this is the first failure
// of a run of them, so the log says it once.
func (b *embedBreaker) failed(now time.Time) (first bool, wait time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	first = b.delay == 0
	if b.delay == 0 {
		b.delay = embedBackoffStart
	} else if b.delay *= 2; b.delay > embedBackoffMax {
		b.delay = embedBackoffMax
	}
	b.until = now.Add(b.delay)
	return first, b.delay
}

func (b *embedBreaker) succeeded() (recovered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recovered = b.delay != 0
	b.delay, b.until = 0, time.Time{}
	return recovered
}

// embed returns text's vector when it is worth computing: there is an
// embedder, the store does not embed for itself, and the embedder has not
// just failed. Otherwise it returns false at once and the caller searches by
// text, as it would without an embedder.
func (s *Service) embed(ctx context.Context, text string) ([]float64, bool) {
	if s == nil || s.embedder == nil {
		return nil, false
	}
	if se, ok := s.store.(domain.MemoryServerEmbedding); ok && se.EmbedsServerSide() {
		return nil, false
	}
	// No model configured is not a failing model: a standalone install with
	// none used to log "embedder failing" at its first question.
	if ec, ok := s.embedder.(domain.EmbedderConfigured); ok && !ec.EmbeddingConfigured() {
		return nil, false
	}
	if s.embedState.open(time.Now()) {
		return nil, false
	}
	vec, err := s.embedder.Embed(ctx, text)
	if err != nil || len(vec) == 0 {
		if ctx.Err() != nil {
			return nil, false // the caller gave up; that says nothing about the embedder
		}
		if first, wait := s.embedState.failed(time.Now()); first {
			agentgolog.WithModule("memory").Warn("embedder failing; searching by text until it answers",
				"retry_in", wait.String(), "error", err)
		}
		return nil, false
	}
	if s.embedState.succeeded() {
		agentgolog.WithModule("memory").Info("embedder answering again")
	}
	return vec, true
}
