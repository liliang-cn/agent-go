package agent

import (
	"context"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// KnowledgeRecall does graph-aware recall over the agent's long-term memory:
// when the configured store is graph-capable (WithGraphMemory / graphflow) it
// returns fused memories + entities + knowledge-graph relations; otherwise it
// degrades gracefully to plain memory search. This is the library-facing entry
// that lets the agent loop "know" the knowledge graph.
func (s *Service) KnowledgeRecall(ctx context.Context, query string, topK int) (*domain.GraphRecallResult, error) {
	if s == nil || s.memoryService == nil {
		return nil, fmt.Errorf("memory is not enabled for this agent")
	}
	if r, ok := s.memoryService.(interface {
		KnowledgeRecall(context.Context, string, int) (*domain.GraphRecallResult, error)
	}); ok {
		return r.KnowledgeRecall(ctx, query, topK)
	}
	return nil, fmt.Errorf("memory service does not support knowledge recall")
}

// RegisterGraphRecallTool registers the `graph_recall` tool so the model can
// query long-term memory + the knowledge graph from inside the agent loop. It
// works with any memory store (graph stores return entities/relations; others
// degrade to plain recall). WithGraphMemory() registers it automatically.
func RegisterGraphRecallTool(svc *Service) {
	if svc == nil {
		return
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("graph_recall") {
		return
	}
	svc.AddToolWithMetadata(
		"graph_recall",
		"查询长期记忆与知识图谱：返回相关记忆、识别出的实体、以及实体之间的关系（知识图谱扩展）。当问题涉及"+
			"\"谁/和谁/什么关系/相关的人或事/之前提到的某人某事\"时优先调用，比单纯关键词检索更能顺着关系找到答案。",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "检索问题（自然语言）"},
				"top_k": map[string]interface{}{"type": "integer", "description": "返回条数，默认 8"},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			query := toolArgString(args, "query")
			if query == "" {
				return map[string]interface{}{"ok": false, "error": "query is required"}, nil
			}
			topK := toolArgInt(args, "top_k")
			if topK <= 0 {
				topK = 8
			}
			res, err := svc.KnowledgeRecall(ctx, query, topK)
			if err != nil {
				return map[string]interface{}{"ok": false, "error": err.Error()}, nil
			}
			memories := make([]string, 0, len(res.Memories))
			for _, m := range res.Memories {
				if m != nil && m.Memory != nil {
					memories = append(memories, m.Memory.Content)
				}
			}
			return map[string]interface{}{
				"ok": true,
				"data": map[string]interface{}{
					"entities":  res.Entities,
					"knowledge": res.Knowledge,
					"memories":  memories,
					"context":   res.ContextText,
				},
			}, nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}
