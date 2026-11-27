package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	rpcv1 "github.com/liliang-cn/cortexdb/v2/pkg/rpc/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The brain's knowledge_memory_recall, as far as this store reads it.
type remoteRecallResponse struct {
	Memories []struct {
		Memory struct {
			ID         string         `json:"id"`
			SessionID  string         `json:"session_id"`
			Scope      string         `json:"scope"`
			Content    string         `json:"content"`
			Metadata   map[string]any `json:"metadata"`
			Importance float64        `json:"importance"`
			CreatedAt  time.Time      `json:"created_at"`
		} `json:"memory"`
		Score float64 `json:"score"`
	} `json:"memories"`
	Knowledge []struct {
		KnowledgeID string   `json:"knowledge_id"`
		Title       string   `json:"title"`
		Snippet     string   `json:"snippet"`
		Score       float64  `json:"score"`
		Entities    []string `json:"entities"`
	} `json:"knowledge"`
	Entities      []string `json:"entities"`
	GraphEntities []string `json:"graph_entities"`
	ContextPack   struct {
		Text string `json:"text"`
	} `json:"context_pack"`
}

// KnowledgeRecall runs the brain's own recall: its memories and its
// knowledge, and — with the brain's embedder — the vector search followed by
// a walk of the entity graph from what it found, ranked by both. SearchByText
// reaches the memories alone. Implements domain.KnowledgeGraphRecaller.
func (s *CortexRemoteMemoryStore) KnowledgeRecall(ctx context.Context, query string, topK int, _ *domain.MemoryScope) (*domain.GraphRecallResult, error) {
	out := &domain.GraphRecallResult{Query: query}
	if strings.TrimSpace(query) == "" {
		return out, nil
	}
	if topK <= 0 {
		topK = 10
	}
	args, err := json.Marshal(map[string]any{
		"query":           query,
		"user_id":         s.cfg.UserID,
		"scope":           s.cfg.Scope,
		"namespace":       s.cfg.Namespace,
		"top_k_memories":  topK,
		"top_k_knowledge": topK,
		"retrieval_mode":  "auto",
	})
	if err != nil {
		return nil, err
	}
	callCtx, cancel := s.call(ctx)
	defer cancel()
	resp, err := s.tools.CallTool(callCtx, &rpcv1.CallToolRequest{Name: "knowledge_memory_recall", ArgsJson: string(args)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, fmt.Errorf("cortex-remote knowledge_memory_recall: %w", domain.ErrMemoryStoreUnsupported)
		}
		return nil, fmt.Errorf("cortex-remote knowledge_memory_recall: %w", err)
	}
	var r remoteRecallResponse
	if err := json.Unmarshal([]byte(resp.GetResultJson()), &r); err != nil {
		return nil, fmt.Errorf("cortex-remote knowledge_memory_recall: decode: %w", err)
	}
	out.Entities = r.Entities
	if len(r.GraphEntities) > 0 {
		out.Entities = mergeEntityNames(r.GraphEntities, r.Entities)
	}
	out.ContextText = r.ContextPack.Text
	for _, h := range r.Memories {
		if h.Memory.ID == "" {
			continue
		}
		m := &domain.Memory{
			ID:         h.Memory.ID,
			Content:    h.Memory.Content,
			Importance: h.Memory.Importance,
			Type:       domain.MemoryTypeFact,
			CreatedAt:  h.Memory.CreatedAt,
			UpdatedAt:  h.Memory.CreatedAt,
		}
		applyRemoteMetadata(m, h.Memory.Metadata, h.Memory.Scope, h.Memory.SessionID)
		out.Memories = append(out.Memories, &domain.MemoryWithScore{Memory: m, Score: h.Score})
	}
	for _, k := range r.Knowledge {
		out.Knowledge = append(out.Knowledge, domain.GraphKnowledgeHit{
			ID: k.KnowledgeID, Title: k.Title, Snippet: k.Snippet, Score: k.Score, Entities: k.Entities,
		})
	}
	return out, nil
}

// mergeEntityNames is the walk's entities, best first, then the rest, each
// once.
func mergeEntityNames(first, rest []string) []string {
	seen := make(map[string]bool, len(first)+len(rest))
	out := make([]string, 0, len(first)+len(rest))
	for _, list := range [][]string{first, rest} {
		for _, n := range list {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

var _ domain.KnowledgeGraphRecaller = (*CortexRemoteMemoryStore)(nil)
