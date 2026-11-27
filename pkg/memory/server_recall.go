package memory

import (
	"context"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// knowledgeHitIDPrefix marks a knowledge hit carried in the memory list: it
// is a document of the brain's, not a memory of this store's.
const knowledgeHitIDPrefix = "kn_"

// serverRecall is the recall of a store that embeds on its own side and
// recalls through its own graph (a shared CortexDB brain). Its memories pass
// the scope chain like any other; its knowledge hits have no scope and come
// along as facts. The two lists are ranked by the brain separately, so they
// are interleaved by rank and scored by position: their scores are on
// different scales and would sort one kind wholly above the other.
func (s *Service) serverRecall(ctx context.Context, query string, scopes []domain.MemoryScope, tally *recallTally) ([]*domain.MemoryWithScore, bool) {
	se, ok := s.store.(domain.MemoryServerEmbedding)
	if !ok || !se.EmbedsServerSide() {
		return nil, false
	}
	recaller, ok := s.store.(domain.KnowledgeGraphRecaller)
	if !ok {
		return nil, false
	}
	done := trace.Step(ctx, "memory", "recall-server")
	res, err := recaller.KnowledgeRecall(ctx, query, s.maxMemories, nil)
	done(err)
	if err != nil || res == nil {
		return nil, false
	}
	mems := tally.scoped(res.Memories, scopes)
	var knowledge []*domain.MemoryWithScore
	for _, k := range res.Knowledge {
		if k.ID == "" {
			continue
		}
		content := strings.TrimSpace(k.Snippet)
		if t := strings.TrimSpace(k.Title); t != "" {
			content = t + ": " + content
		}
		knowledge = append(knowledge, &domain.MemoryWithScore{
			Memory: &domain.Memory{ID: knowledgeHitIDPrefix + k.ID, Type: domain.MemoryTypeFact, Content: content},
		})
	}
	out := make([]*domain.MemoryWithScore, 0, len(mems)+len(knowledge))
	for i := 0; i < len(mems) || i < len(knowledge); i++ {
		if i < len(mems) {
			out = append(out, mems[i])
		}
		if i < len(knowledge) {
			out = append(out, knowledge[i])
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	for i, m := range out {
		m.Score = 1 - float64(i)*0.01
	}
	return out, true
}
