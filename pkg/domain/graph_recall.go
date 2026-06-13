package domain

import "context"

// GraphKnowledgeHit is a provider-agnostic knowledge-graph search hit returned
// by a graph-aware memory store. It deliberately avoids leaking the cortexdb
// types so the agent loop and library users depend only on pkg/domain.
type GraphKnowledgeHit struct {
	ID       string   `json:"id,omitempty"`
	Title    string   `json:"title,omitempty"`
	Snippet  string   `json:"snippet,omitempty"`
	Score    float64  `json:"score"`
	Entities []string `json:"entities,omitempty"`
}

// GraphRecallResult is the fused memory + knowledge-graph recall result. For a
// non-graph store it degrades to just Memories (Entities/Knowledge empty).
type GraphRecallResult struct {
	Query       string              `json:"query"`
	Entities    []string            `json:"entities,omitempty"`
	Memories    []*MemoryWithScore  `json:"memories,omitempty"`
	Knowledge   []GraphKnowledgeHit `json:"knowledge,omitempty"`
	ContextText string              `json:"context_text,omitempty"` // ready-to-inject context pack
}

// KnowledgeGraphRecaller is the optional capability a memory store / service
// implements when it can do graph-aware recall (entities + relations, not just
// vector similarity). The runtime and Service type-assert to this; stores that
// don't implement it simply don't expose graph recall.
type KnowledgeGraphRecaller interface {
	KnowledgeRecall(ctx context.Context, query string, topK int, scope *MemoryScope) (*GraphRecallResult, error)
}
