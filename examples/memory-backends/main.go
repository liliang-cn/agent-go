// Picking a memory backend, and writing your own.
//
//	STORE_TYPE=qdrant STORE_DSN=http://192.168.1.10:6333 go run ./examples/memory-backends
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	_ "github.com/liliang-cn/agent-go/v3/pkg/store" // registers the shipped backends
)

func main() {
	ctx := context.Background()

	// ------------------------------------------------------------------
	// 1. Using one. This is the whole integration: a name and an address.
	// ------------------------------------------------------------------
	svc, err := agent.New("assistant").
		WithMemory(
			agent.WithMemoryStoreType(envOr("STORE_TYPE", "qdrant")),
			agent.WithMemoryDSN(envOr("STORE_DSN", "http://127.0.0.1:6333")),
			// Anything the backend needs beyond an address. Every key is
			// optional and documented on the backend's Config type.
			agent.WithMemoryOptions(map[string]string{
				"collection": "agentgo_memories",
			}),
		).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	// From here nothing knows which backend it is. Memory is retrieved on
	// every turn and written in the background; a run just runs.
	if res, err := svc.Run(ctx, "Remember that the deploy key lives in 1Password."); err == nil {
		fmt.Println("stored:", res.Text())
	}

	// ------------------------------------------------------------------
	// 2. Swapping one at runtime, without rebuilding the agent.
	// ------------------------------------------------------------------
	//   previous := svc.SetMemoryService(other)  // drained and closed
	//   svc.SetMemoryService(nil)                // memory off; runs carry on

	// ------------------------------------------------------------------
	// 3. Writing your own. A registration, never a new case in a switch.
	// ------------------------------------------------------------------
	if err := agent.RegisterMemoryStore("in-memory-demo", func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		// cfg carries Name, Path, DSN, Options, and — when the host has
		// them — an Embedder and a Generator, so a backend that wants to
		// embed can, and one that does not can ignore them.
		return &demoStore{byID: map[string]*domain.Memory{}}, nil
	}); err != nil {
		log.Fatal(err)
	}

	own, err := agent.New("own-backend").
		WithMemory(agent.WithMemoryStoreType("in-memory-demo")).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer own.Close()
	fmt.Println("a backend of your own is registered and built")
}

// demoStore is the smallest possible backend.
//
// It embeds memory.BaseStore, whose eighteen methods all return
// ErrMemoryStoreUnsupported, and overrides the four that matter. That is the
// intended shape: implement what you can do, say so about the rest, and let
// callers degrade — an honest unsupported beats a fake implementation.
type demoStore struct {
	memory.BaseStore
	byID map[string]*domain.Memory
}

func (d *demoStore) Store(_ context.Context, m *domain.Memory) error {
	if m.ID == "" {
		m.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	clone := *m
	d.byID[m.ID] = &clone
	return nil
}

func (d *demoStore) Get(_ context.Context, id string) (*domain.Memory, error) {
	m, ok := d.byID[id]
	if !ok {
		return nil, fmt.Errorf("demo: %s not found", id)
	}
	return m, nil
}

// SearchByText is the one that decides whether the agent ever sees anything.
// The memory service calls it when there is no embedder, or when the vector
// path returns nothing — which is every backend that ranks lexically.
func (d *demoStore) SearchByText(_ context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	out := make([]*domain.MemoryWithScore, 0, len(d.byID))
	for _, m := range d.byID {
		// A real backend ranks here. Scope filtering is NOT this method's
		// job: agent-go applies the query's scope chain to the results, and
		// a backend that filters by session as well hides the global
		// memories a session is entitled to see.
		out = append(out, &domain.MemoryWithScore{Memory: m, Score: 1})
		if len(out) >= topK {
			break
		}
	}
	return out, nil
}

func (d *demoStore) List(_ context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	all := make([]*domain.Memory, 0, len(d.byID))
	for _, m := range d.byID {
		all = append(all, m)
	}
	if offset >= len(all) {
		return nil, len(all), nil
	}
	end := offset + limit
	if limit <= 0 || end > len(all) {
		end = len(all)
	}
	return all[offset:end], len(all), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
