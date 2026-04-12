package store

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/graphflow"
)

// GraphFlowStore stores AgentGo memories in CortexDB and mirrors each memory
// into CortexDB's GraphFlow graph extraction/build pipeline.
type GraphFlowStore struct {
	*MemoryStore
	extractor graphflow.Extractor
}

func NewGraphFlowStore(dbPath string) (*GraphFlowStore, error) {
	base, err := NewMemoryStore(dbPath)
	if err != nil {
		return nil, err
	}

	return &GraphFlowStore{
		MemoryStore: base,
		extractor:   graphflow.HeuristicExtractor{},
	}, nil
}

func (s *GraphFlowStore) Store(ctx context.Context, memory *domain.Memory) error {
	if err := s.MemoryStore.Store(ctx, memory); err != nil {
		return err
	}
	return s.buildMemoryGraph(ctx, memory)
}

func (s *GraphFlowStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if err := s.MemoryStore.StoreWithScope(ctx, memory, scope); err != nil {
		return err
	}
	return s.buildMemoryGraph(ctx, memory)
}

func (s *GraphFlowStore) Update(ctx context.Context, memory *domain.Memory) error {
	if err := s.MemoryStore.Update(ctx, memory); err != nil {
		return err
	}
	return s.buildMemoryGraph(ctx, memory)
}

func (s *GraphFlowStore) buildMemoryGraph(ctx context.Context, memory *domain.Memory) error {
	if s == nil || s.db == nil || memory == nil || memory.Content == "" {
		return nil
	}

	doc := graphflow.SourceDocument{
		ID:      "memory:" + memory.ID,
		Path:    "memory:" + memory.ID,
		Type:    "memory",
		Title:   string(memory.Type),
		Content: memory.Content,
		Metadata: map[string]string{
			"memory_id":   memory.ID,
			"memory_type": string(memory.Type),
		},
	}
	extraction, err := s.extractor.Extract(ctx, doc)
	if err != nil {
		return fmt.Errorf("extract memory graph: %w", err)
	}
	_, err = graphflow.Build(ctx, s.db, []graphflow.ExtractionResult{*extraction}, graphflow.BuildOptions{
		Collection: "agentgo-memory",
	})
	if err != nil {
		return fmt.Errorf("build memory graph: %w", err)
	}
	return nil
}
