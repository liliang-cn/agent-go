package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// What a trace says about recalled memory: which ids reached the prompt, with
// what score and from which scope, and how many candidates fell on the way.
// Never the content — that is user data, and payload mode is a separate switch.

// recallTally counts what the pipeline dropped before the final list. A store
// or navigator that filters by scope on its own side cannot be counted; those
// candidates never reach this layer.
type recallTally struct {
	droppedByScope int
	droppedByCap   int
	// seen is every candidate that reached the merge, before validity, noise,
	// scoring and the cap.
	seen int
}

// scoped applies the scope filter and counts what it removed.
func (t *recallTally) scoped(mems []*domain.MemoryWithScore, scopes []domain.MemoryScope) []*domain.MemoryWithScore {
	out := filterMemoriesByScopes(mems, scopes)
	t.droppedByScope += len(mems) - len(out)
	return out
}

func (t *recallTally) scopedPlain(mems []*domain.Memory, scopes []domain.MemoryScope) []*domain.Memory {
	out := filterPlainMemoriesByScopes(mems, scopes)
	t.droppedByScope += len(mems) - len(out)
	return out
}

// fileContextStats says what the file backend put above the ranked list.
type fileContextStats struct {
	entrypoint          bool
	sessionMemory       bool
	headerIDs           []string
	headersCandidates   int
	headersDroppedScope int
}

func scopeChainDetail(scopes []domain.MemoryScope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if strings.TrimSpace(s.ID) == "" {
			out = append(out, string(s.Type))
		} else {
			out = append(out, string(s.Type)+":"+s.ID)
		}
	}
	return out
}

func memoryScopeOf(m *domain.Memory) domain.MemoryScope {
	scope := ParseBankID(m.SessionID)
	if n := normalizeScopeType(m.ScopeType); n != domain.MemoryScopeGlobal || strings.TrimSpace(m.ScopeID) != "" {
		scope = domain.MemoryScope{Type: n, ID: strings.TrimSpace(m.ScopeID)}
	}
	return scope
}

// traceInjection writes the inject_memories decision for one retrieval.
func traceInjection(ctx context.Context, scopes []domain.MemoryScope, injected []*domain.MemoryWithScore, tally recallTally, fx fileContextStats, why string) {
	items := make([]map[string]any, 0, len(injected))
	for _, m := range injected {
		if m == nil || m.Memory == nil {
			continue
		}
		sc := memoryScopeOf(m.Memory)
		item := map[string]any{
			"id":         m.ID,
			"score":      float64(int(m.Score*1000+0.5)) / 1000,
			"scope_type": string(sc.Type),
		}
		if sc.ID != "" {
			item["scope_id"] = sc.ID
		}
		if m.Type != "" {
			item["type"] = string(m.Type)
		}
		items = append(items, item)
	}
	detail := map[string]any{
		"injected": items,
		"counts": map[string]any{
			"candidates":               tally.seen + tally.droppedByScope,
			"dropped_by_scope":         tally.droppedByScope,
			"dropped_by_cap":           tally.droppedByCap,
			"dropped_other":            max(0, tally.seen-tally.droppedByCap-len(items)),
			"injected":                 len(items),
			"header_candidates":        fx.headersCandidates,
			"headers_dropped_by_scope": fx.headersDroppedScope,
		},
		"query_scopes":     scopeChainDetail(scopes),
		"entrypoint_index": fx.entrypoint,
		"session_memory":   fx.sessionMemory,
		"selected_headers": fx.headerIDs,
	}
	reason := fmt.Sprintf("%d memories injected", len(items))
	if len(items) > 0 {
		parts := make([]string, 0, len(items))
		for _, it := range items {
			sc := it["scope_type"].(string)
			if id, ok := it["scope_id"].(string); ok {
				sc += ":" + id
			}
			parts = append(parts, fmt.Sprintf("%v(%v,%s)", it["id"], it["score"], sc))
		}
		reason += ": " + strings.Join(parts, " ")
	}
	if tally.droppedByScope > 0 || tally.droppedByCap > 0 {
		reason += fmt.Sprintf(" [dropped: scope=%d cap=%d]", tally.droppedByScope, tally.droppedByCap)
	}
	if len(items) == 0 {
		if why == "" {
			why = "nothing matched"
		}
		reason = "no memories injected: " + why
	}
	trace.Decide(ctx, "inject_memories", reason, detail)
}

// TraceNoInjection lets a caller that never reached retrieval (no memory
// service, a timeout) say so in the same shape.
func TraceNoInjection(ctx context.Context, why string) {
	trace.Decide(ctx, "inject_memories", "no memories injected: "+why, map[string]any{
		"injected": []map[string]any{},
		"counts":   map[string]any{"candidates": 0, "dropped_by_scope": 0, "dropped_by_cap": 0, "injected": 0},
	})
}
