package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// AddReconciled saves a memory the agent chose to save, reconciled against
// what is already remembered — the explicit counterpart of the automatic
// writer's reconciliation (reconcile.go).
//
// Measured in superai: the model saved "moved to Beijing" with memory_save,
// the automatic writer then extracted the same fact, and "I live in Chengdu"
// stayed current beside two Beijing rows, because the explicit path added
// blindly. It now asks one closed question over the same candidates the
// automatic writer sees.
//
// The memory is stored at once and the question is asked after, in the
// background. Asked first, it stood between the tool call and the turn: on
// deepseek-flash memory_save took 19.4s of a 24.9s turn, every turn that
// saved anything, while the person waited for an answer that did not depend
// on it. What the verdict changes is applied when it comes: "update" retires
// the memory it replaces, "noop" removes the copy just stored, and the graph
// is written. Close waits for verdicts still out.
//
// No model, no candidates, or an answer that does not parse: the memory
// stays as stored. A reconciliation that cannot run must not lose it.
func (s *Service) AddReconciled(ctx context.Context, mem *domain.Memory) (domain.MemoryReconcileOutcome, error) {
	if mem == nil {
		return domain.MemoryReconcileOutcome{}, fmt.Errorf("memory is nil")
	}
	if err := s.Add(ctx, mem); err != nil {
		return domain.MemoryReconcileOutcome{}, err
	}
	out := domain.MemoryReconcileOutcome{Op: domain.MemoryOpAdd, ID: mem.ID}
	if s.llm == nil || strings.TrimSpace(mem.Content) == "" {
		return out, nil
	}
	s.inBackground(ctx, func(ctx context.Context) { s.reconcileSaved(ctx, mem) })
	return out, nil
}

// reconcileSaved asks about a memory already stored and applies the answer.
func (s *Service) reconcileSaved(ctx context.Context, mem *domain.Memory) {
	// Asked even when nothing similar is stored: the same call says what the
	// memory is about, and a first memory needs its graph as much as any.
	var candidates []*domain.Memory
	for _, c := range s.reconcileCandidates(ctx, explicitStoreRequest(mem)) {
		if c.ID != mem.ID {
			candidates = append(candidates, c)
		}
	}
	verdict, ok := s.askExplicitReconcile(ctx, mem.Content, candidates)
	if !ok {
		return
	}
	known := map[string]bool{}
	for _, c := range candidates {
		known[c.ID] = true
	}
	if verdict.Op == domain.MemoryOpNoop && known[verdict.TargetID] {
		if err := s.Delete(ctx, mem.ID); err != nil {
			agentgolog.WithModule("memory.autostore").Warn("an explicit save repeats a stored memory; the copy stays",
				"memory_id", mem.ID, "matches", verdict.TargetID, "error", err)
		}
		return
	}
	graph := withoutIDNames(domain.MemoryGraph{Entities: verdict.Entities, Relations: verdict.Relations})
	if names := graph.Names(); len(names) > 0 {
		mem.Keywords = mergeUniqueStrings(mem.Keywords, names)
		_ = s.store.Update(ctx, mem)
	}
	s.writeMemoryGraph(ctx, mem.ID, graph)
	// An "update" naming a memory the model was not shown is an add.
	if verdict.Op == domain.MemoryOpUpdate && known[verdict.TargetID] {
		s.supersedeOrKeep(ctx, verdict.TargetID, mem.ID)
	}
}

// explicitStoreRequest turns the memory's own scope into the request shape
// reconcileCandidates reads, so candidates come from the scopes this memory
// will live in.
func explicitStoreRequest(mem *domain.Memory) *domain.MemoryStoreRequest {
	req := &domain.MemoryStoreRequest{TaskGoal: mem.Content}
	id := strings.TrimSpace(mem.ScopeID)
	switch mem.ScopeType {
	case domain.MemoryScopeSession:
		req.SessionID = id
	case domain.MemoryScopeAgent:
		req.AgentID = id
	case domain.MemoryScopeTeam:
		req.TeamID = id
	case domain.MemoryScopeUser:
		req.UserID = id
	}
	if req.SessionID == "" {
		req.SessionID = strings.TrimSpace(mem.SessionID)
	}
	return req
}

type explicitVerdict struct {
	Op       string `json:"op"`
	TargetID string `json:"target_id"`
	// What the memory is about, asked in the same call (see graph.go).
	Entities  []domain.MemoryGraphEntity   `json:"graph_entities"`
	Relations []domain.MemoryGraphRelation `json:"graph_relations"`
}

func (s *Service) askExplicitReconcile(ctx context.Context, content string, candidates []*domain.Memory) (explicitVerdict, bool) {
	var b strings.Builder
	b.WriteString("A memory is about to be saved:\n")
	b.WriteString(oneLine(content))
	b.WriteString("\n\nMemories already stored, each with its id:\n")
	if len(candidates) == 0 {
		b.WriteString("(none — the op is \"add\")\n")
	}
	for _, c := range candidates {
		fmt.Fprintf(&b, "- [%s] %s\n", c.ID, oneLine(c.Content))
	}
	b.WriteString(`
Decide one op:
- "noop": a stored memory already says the same thing; set target_id to it. Nothing is saved.
- "update": the new memory replaces a stored one that is now out of date (the same attribute of the same subject with a new value); set target_id to the replaced memory. The new memory is saved and the old one retired, so the new content must stand on its own.
- "add": neither; the new memory is saved beside the others. Leave target_id empty.
Judge by meaning, in whatever language the memories are written.

Also fill "graph_entities" and "graph_relations" with what the memory about to be saved is about:
` + graphRuleBody())

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"op":        map[string]interface{}{"type": "string", "enum": []string{domain.MemoryOpAdd, domain.MemoryOpUpdate, domain.MemoryOpNoop}},
			"target_id": map[string]interface{}{"type": "string"},
		},
		"required": append([]string{"op", "target_id"}, graphRequiredFields()...),
	}
	for name, spec := range graphSchemaFields() {
		schema["properties"].(map[string]interface{})[name] = spec
	}
	result, err := s.llm.GenerateStructured(ctx, b.String(), schema, &domain.GenerationOptions{Temperature: 0})
	if err != nil || result == nil || !result.Valid || strings.TrimSpace(result.Raw) == "" {
		agentgolog.WithModule("memory.reconcile").Warn("explicit-save reconciliation unavailable; adding", "error", err)
		return explicitVerdict{}, false
	}
	var v explicitVerdict
	if err := json.Unmarshal([]byte(extractJSONObject(result.Raw)), &v); err != nil {
		return explicitVerdict{}, false
	}
	v.Op = strings.ToLower(strings.TrimSpace(v.Op))
	v.TargetID = strings.TrimSpace(v.TargetID)
	return v, true
}

// extractJSONObject trims anything around the outermost object; structured
// output from some providers arrives fenced.
func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i >= 0 && j > i {
		return raw[i : j+1]
	}
	return raw
}
