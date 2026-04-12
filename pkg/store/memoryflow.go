package store

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/memoryflow"
)

// MemoryFlowStore stores AgentGo memories through CortexDB's MemoryFlow workflow
// while preserving the existing MemoryStore interface used by memory.Service.
type MemoryFlowStore struct {
	*MemoryStore
	flow *memoryflow.Service
}

func NewMemoryFlowStore(dbPath string) (*MemoryFlowStore, error) {
	base, err := NewMemoryStore(dbPath)
	if err != nil {
		return nil, err
	}

	flow, err := memoryflow.New(
		base.db,
		nil,
		nil,
		memoryflow.WithConventions(memoryflow.DefaultConventionSet("agentgo")),
	)
	if err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("create memoryflow service: %w", err)
	}

	return &MemoryFlowStore{MemoryStore: base, flow: flow}, nil
}

func (s *MemoryFlowStore) Store(ctx context.Context, memory *domain.Memory) error {
	return s.StoreWithScope(ctx, memory, resolveMemoryScope(memory))
}

func (s *MemoryFlowStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return fmt.Errorf("memory is required")
	}
	if memory.ID == "" {
		return fmt.Errorf("memory id is required")
	}
	if memory.Content == "" {
		return fmt.Errorf("memory content is required")
	}

	scope = normalizeVectorScope(scope)
	metadata := buildStoredMemoryMetadata(memory, scope, logicalBankIDFromScope(scope))

	req := memoryflow.DiaryEntryRequest{
		EntryID:    memory.ID,
		Scope:      encodedScopeForBucket(scope),
		Namespace:  memoryBucketNamespace,
		Content:    memory.Content,
		Metadata:   metadata,
		Importance: memory.Importance,
	}
	switch scope.Type {
	case domain.MemoryScopeSession:
		req.SessionID = scope.ID
	case domain.MemoryScopeGlobal:
	default:
		req.UserID = encodedUserIDForScope(scope)
	}

	_, err := s.flow.AppendDiaryEntry(ctx, req)
	return err
}
