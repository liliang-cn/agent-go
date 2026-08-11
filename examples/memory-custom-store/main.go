// Command memory-custom-store shows the smallest possible pluggable memory
// backend: a store that implements three of domain.MemoryStore's eighteen
// methods by embedding memory.BaseStore, registers itself under a store_type
// name, and is then selected by configuration alone.
//
//	go run ./examples/memory-custom-store
//
// Three seams exist, in decreasing order of how much the framework still does
// for you; this example demonstrates the first two.
//
//  1. agent.RegisterMemoryStore(name, factory) — config-selectable backend.
//  2. agent.WithMemoryStore(instance)          — hand over a ready instance.
//  3. builder.WithMemoryService(service)       — replace the memory service.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
)

// ============================================================
// A memory backend in ~40 lines
// ============================================================

// jsonlStore keeps memories in RAM and appends them to a JSONL file. It embeds
// memory.BaseStore, so the fifteen methods it does not implement answer
// memory.ErrMemoryStoreUnsupported instead of having to be written out.
type jsonlStore struct {
	memory.BaseStore

	mu    sync.Mutex
	path  string
	seq   int
	items map[string]*domain.Memory
	order []string
}

func newJSONLStore(path string) (*jsonlStore, error) {
	if path == "" {
		return nil, fmt.Errorf("jsonl memory store: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &jsonlStore{path: path, items: map[string]*domain.Memory{}}, nil
}

func (s *jsonlStore) Store(_ context.Context, m *domain.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		s.seq++
		m.ID = fmt.Sprintf("mem-%d", s.seq)
	}
	if _, seen := s.items[m.ID]; !seen {
		s.order = append(s.order, m.ID)
	}
	clone := *m
	s.items[m.ID] = &clone

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "%s\t%s\n", m.ID, m.Content)
	return err
}

func (s *jsonlStore) SearchByText(_ context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.MemoryWithScore
	for _, id := range s.order {
		m := s.items[id]
		if strings.Contains(strings.ToLower(m.Content), strings.ToLower(query)) {
			clone := *m
			out = append(out, &domain.MemoryWithScore{Memory: &clone, Score: 1.0})
			if len(out) >= topK {
				break
			}
		}
	}
	return out, nil
}

func (s *jsonlStore) Get(_ context.Context, id string) (*domain.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("memory %q not found", id)
	}
	clone := *m
	return &clone, nil
}

// InitSchema is called once by the builder. Returning nil means "nothing to do";
// returning memory.ErrMemoryStoreUnsupported would be tolerated too.
func (s *jsonlStore) InitSchema(context.Context) error { return nil }

// Registering in init() means importing this package is all it takes for
// `store_type = "jsonl"` to become selectable.
func init() {
	agent.MustRegisterMemoryStore("jsonl", func(cfg agent.MemoryStoreConfig) (domain.MemoryStore, error) {
		path := cfg.OptionOr("file", filepath.Join(cfg.Path, "memories.jsonl"))
		return newJSONLStore(path)
	})
}

// ============================================================

func main() {
	home, err := os.MkdirTemp("", "agentgo-custom-memory-*")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	fmt.Println("registered memory backends:", agent.RegisteredMemoryStores())

	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()

	// --- Seam 1: selected by name, through configuration. ---
	svc, err := agent.New("custom-memory-agent").
		WithConfig(cfg).
		WithMemory(
			agent.WithMemoryStoreType("jsonl"),
			agent.WithMemoryOption("file", filepath.Join(home, "memories.jsonl")),
		).
		Build()
	if err != nil {
		log.Fatalf("build: %v", err)
	}

	ctx := context.Background()
	mems := svc.Memory
	if mems == nil {
		log.Fatal("memory service is nil")
	}
	if err := mems.Add(ctx, &domain.Memory{
		Type:    domain.MemoryTypePreference,
		Content: "Liang uses random high ports like 3076 and 43510, never 8080.",
	}); err != nil {
		log.Fatalf("add: %v", err)
	}
	hits, err := mems.Search(ctx, "high ports", 5)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	for _, h := range hits {
		fmt.Printf("seam 1 (registry) hit: %.2f %s\n", h.Score, h.Content)
	}

	blob, err := os.ReadFile(filepath.Join(home, "memories.jsonl"))
	if err != nil {
		log.Fatalf("read jsonl: %v", err)
	}
	fmt.Printf("seam 1 wrote to disk: %s", blob)

	// --- Seam 2: hand over an instance the caller already owns. ---
	instance, err := newJSONLStore(filepath.Join(home, "injected.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	svc2, err := agent.New("injected-memory-agent").
		WithConfig(cfg).
		WithMemory(agent.WithMemoryStore(instance)).
		Build()
	if err != nil {
		log.Fatalf("build injected: %v", err)
	}
	if err := svc2.Memory.Add(ctx, &domain.Memory{
		Type:    domain.MemoryTypeFact,
		Content: "This one went through WithMemoryStore, no factory involved.",
	}); err != nil {
		log.Fatalf("add injected: %v", err)
	}
	blob, _ = os.ReadFile(filepath.Join(home, "injected.jsonl"))
	fmt.Printf("seam 2 wrote to disk: %s", blob)

	// --- What BaseStore does for the methods you did not write. ---
	err = instance.Clear(ctx)
	fmt.Println("unimplemented Clear():", err)
	fmt.Println("memory.IsUnsupported(err):", memory.IsUnsupported(err))
}
