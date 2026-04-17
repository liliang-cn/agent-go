package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graphflow"
)

// GraphFlowStore stores AgentGo memories in CortexDB and mirrors each memory
// into CortexDB's GraphFlow graph extraction/build pipeline. Search operations
// merge vector results with graph-augmented retrieval via KnowledgeMemory.
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
	return s.buildMemoryGraph(ctx, memory, false)
}

func (s *GraphFlowStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if err := s.MemoryStore.StoreWithScope(ctx, memory, scope); err != nil {
		return err
	}
	return s.buildMemoryGraph(ctx, memory, false)
}

func (s *GraphFlowStore) Update(ctx context.Context, memory *domain.Memory) error {
	if err := s.MemoryStore.Update(ctx, memory); err != nil {
		return err
	}
	// Replace edges on update so stale relationships from the old content
	// don't accumulate in the graph.
	return s.buildMemoryGraph(ctx, memory, true)
}

// Search performs hybrid vector + graph retrieval. First gets vector results
// from the parent, then expands via KnowledgeMemory's graph to find related
// memories that pure vector similarity would miss.
func (s *GraphFlowStore) Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}

	vectorResults, err := s.MemoryStore.Search(ctx, vector, topK, minScore)
	if err != nil {
		return nil, err
	}
	return s.augmentWithGraph(ctx, vectorResults, topK, nil), nil
}

// SearchByScope performs scoped hybrid vector + graph retrieval. Graph-expanded
// chunks are filtered back to the requested scopes to avoid cross-scope leakage.
func (s *GraphFlowStore) SearchByScope(ctx context.Context, vector []float64, scopes []domain.MemoryScope, topK int) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}
	vectorResults, err := s.MemoryStore.SearchByScope(ctx, vector, scopes, topK)
	if err != nil {
		return nil, err
	}
	return s.augmentWithGraph(ctx, vectorResults, topK, scopes), nil
}

// SearchByText performs text search with graph augmentation.
// Results are filtered to memories within the agentgo namespace.
func (s *GraphFlowStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	km := s.db.KnowledgeMemory()
	resp, err := km.Recall(ctx, cortexdb.KnowledgeMemoryRecallRequest{
		Query:        query,
		Namespace:    memoryBucketNamespace,
		TopKMemories: topK,
	})
	if err == nil && resp != nil && len(resp.Memories) > 0 {
		results := make([]*domain.MemoryWithScore, 0, len(resp.Memories))
		for _, hit := range resp.Memories {
			row, loadErr := s.loadStoredMemoryRow(ctx, hit.Memory.ID)
			if loadErr != nil {
				continue
			}
			results = append(results, &domain.MemoryWithScore{
				Memory: row.toDomainMemory(),
				Score:  hit.Score,
			})
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	return s.MemoryStore.SearchByText(ctx, query, topK)
}

// KnowledgeMemoryRecall exposes CortexDB's fused memory+knowledge+graph recall
// with scope awareness.
func (s *GraphFlowStore) KnowledgeMemoryRecall(ctx context.Context, query string, topK int) (*cortexdb.KnowledgeMemoryRecallResponse, error) {
	return s.KnowledgeMemoryRecallScoped(ctx, query, topK, nil)
}

// KnowledgeMemoryRecallScoped runs fused recall restricted to the given scope.
// Pass nil scope for unscoped (namespace-only) recall.
func (s *GraphFlowStore) KnowledgeMemoryRecallScoped(ctx context.Context, query string, topK int, scope *domain.MemoryScope) (*cortexdb.KnowledgeMemoryRecallResponse, error) {
	km := s.db.KnowledgeMemory()
	req := cortexdb.KnowledgeMemoryRecallRequest{
		Query:        query,
		Namespace:    memoryBucketNamespace,
		TopKMemories: topK,
	}
	if scope != nil {
		ns := normalizeVectorScope(*scope)
		req.Scope = encodedScopeForBucket(ns)
		switch ns.Type {
		case domain.MemoryScopeSession:
			req.SessionID = ns.ID
		case domain.MemoryScopeGlobal:
		default:
			req.UserID = encodedUserIDForScope(ns)
		}
	}
	return km.Recall(ctx, req)
}

// augmentWithGraph enriches vector results with graph-expanded neighbors.
// When scopes is non-nil, expanded memories must belong to one of the requested
// scopes or they are dropped (no cross-scope leakage).
func (s *GraphFlowStore) augmentWithGraph(ctx context.Context, vectorResults []*domain.MemoryWithScore, topK int, scopes []domain.MemoryScope) []*domain.MemoryWithScore {
	entityNames := extractEntityNamesFromResults(vectorResults)
	if len(entityNames) == 0 {
		return vectorResults
	}

	km := s.db.KnowledgeMemory()
	expandResp, err := km.ExpandEntityContext(ctx, cortexdb.KnowledgeMemoryExpandEntityContextRequest{
		EntityNames: entityNames,
		MaxHops:     2,
		TopKChunks:  topK,
	})
	if err != nil || expandResp == nil || len(expandResp.Chunks) == 0 {
		return vectorResults
	}

	scopeSet := buildScopeSet(scopes)
	seen := make(map[string]struct{}, len(vectorResults))
	for _, r := range vectorResults {
		seen[r.ID] = struct{}{}
	}

	for _, chunk := range expandResp.Chunks {
		memID := extractMemoryIDFromChunk(chunk)
		if memID == "" || memID == chunk.ID {
			continue
		}
		if _, exists := seen[memID]; exists {
			continue
		}
		row, err := s.loadStoredMemoryRow(ctx, memID)
		if err != nil {
			continue
		}
		mem := row.toDomainMemory()
		// When caller specified scopes, drop any expanded memory that falls
		// outside the requested scope set.
		if scopeSet != nil {
			key := scopeKey(domain.MemoryScope{Type: mem.ScopeType, ID: mem.ScopeID})
			if _, ok := scopeSet[key]; !ok {
				continue
			}
		}
		seen[memID] = struct{}{}
		vectorResults = append(vectorResults, &domain.MemoryWithScore{
			Memory: mem,
			Score:  chunk.Score * 0.8,
		})
	}

	sort.Slice(vectorResults, func(i, j int) bool {
		return vectorResults[i].Score > vectorResults[j].Score
	})
	if len(vectorResults) > topK {
		vectorResults = vectorResults[:topK]
	}
	return vectorResults
}

func buildScopeSet(scopes []domain.MemoryScope) map[string]struct{} {
	if len(scopes) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(scopes))
	for _, sc := range scopes {
		set[scopeKey(normalizeVectorScope(sc))] = struct{}{}
	}
	return set
}

func scopeKey(scope domain.MemoryScope) string {
	return string(scope.Type) + ":" + strings.TrimSpace(scope.ID)
}

func (s *GraphFlowStore) buildMemoryGraph(ctx context.Context, memory *domain.Memory, replace bool) error {
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
			"scope_type":  string(memory.ScopeType),
			"scope_id":    memory.ScopeID,
		},
	}
	extraction, err := s.extractor.Extract(ctx, doc)
	if err != nil {
		return fmt.Errorf("extract memory graph: %w", err)
	}
	_, err = graphflow.Build(ctx, s.db, []graphflow.ExtractionResult{*extraction}, graphflow.BuildOptions{
		Collection:   "agentgo-memory",
		ReplaceEdges: replace,
	})
	if err != nil {
		return fmt.Errorf("build memory graph: %w", err)
	}
	return nil
}

// extractEntityNamesFromResults collects entity-like terms from memory content
// for graph expansion. Prefers explicit tags/keywords, then falls back to
// Capitalized tokens and CJK runs extracted from content.
func extractEntityNamesFromResults(results []*domain.MemoryWithScore) []string {
	if len(results) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var entities []string

	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" {
			return
		}
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		entities = append(entities, term)
	}

	for _, r := range results {
		if r.Memory == nil {
			continue
		}
		for _, kw := range r.Keywords {
			add(kw)
		}
		for _, tag := range r.Tags {
			add(tag)
		}
		// If metadata is sparse, extract surface-level entity terms from content
		// so graph expansion still triggers for unenriched memories.
		for _, term := range extractContentEntities(r.Content) {
			add(term)
		}
	}

	if len(entities) > 10 {
		entities = entities[:10]
	}
	return entities
}

// extractContentEntities pulls CJK runs and Capitalized words from free text.
// Heuristic, not a tagger — good enough to bootstrap graph expansion when
// tags/keywords are missing.
func extractContentEntities(content string) []string {
	if content == "" {
		return nil
	}

	var entities []string
	var cjk strings.Builder
	var word strings.Builder
	firstUpper := false

	flushCJK := func() {
		if cjk.Len() >= 2 {
			entities = append(entities, cjk.String())
		}
		cjk.Reset()
	}
	flushWord := func() {
		if word.Len() >= 3 && firstUpper {
			entities = append(entities, word.String())
		}
		word.Reset()
		firstUpper = false
	}

	for _, r := range content {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			flushWord()
			cjk.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			if word.Len() == 0 && unicode.IsUpper(r) {
				firstUpper = true
			}
			word.WriteRune(r)
		default:
			flushCJK()
			flushWord()
		}
		if len(entities) >= 20 {
			break
		}
	}
	flushCJK()
	flushWord()

	if len(entities) > 20 {
		entities = entities[:20]
	}
	return entities
}

// extractMemoryIDFromChunk tries to find the original memory ID from a graph chunk.
func extractMemoryIDFromChunk(chunk cortexdb.GraphRAGChunkResult) string {
	docID := strings.TrimSpace(chunk.DocumentID)
	if strings.HasPrefix(docID, "memory:") {
		return strings.TrimPrefix(docID, "memory:")
	}
	return ""
}

