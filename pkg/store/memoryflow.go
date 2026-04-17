package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/memoryflow"
)

// MemoryFlowStore stores AgentGo memories through CortexDB's MemoryFlow workflow
// while preserving the existing MemoryStore interface used by memory.Service.
// It overrides search operations to use MemoryFlow's strategy-aware Recall
// and exposes WakeUp/CloseSession for advanced context assembly.
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

// Search and SearchByScope inherit from parent MemoryStore, which uses
// CortexDB's SearchChatHistory with real cosine similarity. MemoryFlow's
// Recall is a text-primary API and doesn't honor the vector/minScore
// contract, so we only use it in SearchByText.

// SearchByText uses MemoryFlow Recall (strategy-planned lexical/BM25) for
// broader recall, then re-scores with n-gram + importance/recency boosts
// for ranking consistent with searchByTextNative.
func (s *MemoryFlowStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if strings.TrimSpace(query) == "" || topK <= 0 {
		return nil, nil
	}

	results, err := s.recallAsSearch(ctx, query, topK)
	if err == nil && len(results) > 0 {
		return results, nil
	}

	return s.MemoryStore.SearchByText(ctx, query, topK)
}

// recallAsSearch converts a MemoryFlow Recall response into domain
// MemoryWithScore results, re-scoring via n-gram + importance/recency
// so ranking matches searchByTextNative.
func (s *MemoryFlowStore) recallAsSearch(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if query == "" {
		query = "*"
	}

	resp, err := s.flow.Recall(ctx, memoryflow.RecallRequest{
		Query:        query,
		Namespace:    memoryBucketNamespace,
		TopKMemories: topK * 2,
		DisableGraph: true,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Response.Memories) == 0 {
		return nil, nil
	}

	tokens := tokenizeText(query)
	now := time.Now()
	results := make([]*domain.MemoryWithScore, 0, len(resp.Response.Memories))
	for _, hit := range resp.Response.Memories {
		row, err := s.loadStoredMemoryRow(ctx, hit.Memory.ID)
		if err != nil {
			continue
		}
		mem := row.toDomainMemory()
		textScore := ngramMatchScore(tokens, mem.Content)
		if textScore == 0 {
			textScore = hit.Score
		}
		score := applyMemoryBoosts(textScore, mem.Importance, mem.CreatedAt, now)
		results = append(results, &domain.MemoryWithScore{
			Memory: mem,
			Score:  score,
		})
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// Reflect uses CortexDB KnowledgeMemory.Consolidate via MemoryFlow context.
func (s *MemoryFlowStore) Reflect(ctx context.Context, bankID string) (string, error) {
	return s.MemoryStore.Reflect(ctx, bankID)
}

// ── Advanced capabilities ────────────────────────────────────────────────

// WakeUp assembles multi-tier startup context using MemoryFlow's WakeUpLayers.
// Pass scope=nil for global (long-term) wake-up, or a specific scope to pull
// per-user / per-session context.
func (s *MemoryFlowStore) WakeUp(ctx context.Context, identity, query string, scope *domain.MemoryScope) ([]WakeUpLayer, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}

	recall := memoryflow.RecallRequest{
		Query:        query,
		Namespace:    memoryBucketNamespace,
		TopKMemories: 8,
	}
	if scope == nil {
		recall.Scope = cortexdb.MemoryScopeGlobal
	} else {
		ns := normalizeVectorScope(*scope)
		recall.Scope = encodedScopeForBucket(ns)
		switch ns.Type {
		case domain.MemoryScopeSession:
			recall.SessionID = ns.ID
		case domain.MemoryScopeGlobal:
		default:
			recall.UserID = encodedUserIDForScope(ns)
		}
	}

	resp, err := s.flow.WakeUpLayers(ctx, memoryflow.WakeUpLayersRequest{
		Identity: identity,
		Recall:   recall,
	})
	if err != nil {
		return nil, err
	}

	layers := make([]WakeUpLayer, 0, len(resp.Layers))
	for _, l := range resp.Layers {
		layers = append(layers, WakeUpLayer{
			Level: string(l.Level),
			Title: l.Title,
			Text:  l.Text,
		})
	}
	return layers, nil
}

// CloseSession promotes extracted knowledge at session end.
func (s *MemoryFlowStore) CloseSession(ctx context.Context, sessionID string, transcript []TranscriptTurn) error {
	turns := make([]memoryflow.TranscriptTurn, 0, len(transcript))
	for _, t := range transcript {
		turns = append(turns, memoryflow.TranscriptTurn{
			Role:      t.Role,
			Content:   t.Content,
			Timestamp: t.Timestamp,
		})
	}

	_, err := s.flow.CloseSession(ctx, memoryflow.CloseSessionRequest{
		Transcript: memoryflow.Transcript{
			SessionID: sessionID,
			Turns:     turns,
		},
		Namespace:  memoryBucketNamespace,
		Promote:    true,
		Collection: "agentgo-memory",
	})
	return err
}

// ── Types for advanced capabilities ──────────────────────────────────────

// WakeUpLayer represents one context density tier from MemoryFlow's WakeUp system.
type WakeUpLayer struct {
	Level string // L0, L1, L2, L3
	Title string
	Text  string
}

// TranscriptTurn is a normalized conversation turn for CloseSession.
type TranscriptTurn struct {
	Role      string
	Content   string
	Timestamp time.Time
}

// ── AdvancedMemoryStore interface ─────────────────────────────────────────

// AdvancedMemoryStore extends domain.MemoryStore with CortexDB advanced capabilities.
type AdvancedMemoryStore interface {
	domain.MemoryStore
	WakeUp(ctx context.Context, identity, query string, scope *domain.MemoryScope) ([]WakeUpLayer, error)
	CloseSession(ctx context.Context, sessionID string, transcript []TranscriptTurn) error
	KnowledgeMemoryRecall(ctx context.Context, query string, topK int) (*cortexdb.KnowledgeMemoryRecallResponse, error)
}

// KnowledgeMemoryRecall exposes CortexDB's fused memory+knowledge+graph recall.
func (s *MemoryFlowStore) KnowledgeMemoryRecall(ctx context.Context, query string, topK int) (*cortexdb.KnowledgeMemoryRecallResponse, error) {
	km := s.db.KnowledgeMemory()
	return km.Recall(ctx, cortexdb.KnowledgeMemoryRecallRequest{
		Query:        query,
		Namespace:    memoryBucketNamespace,
		TopKMemories: topK,
	})
}
