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
// automatic writer sees. That is one model call per explicit save; a save the
// model asked for is rare next to the automatic writer, which runs every turn.
//
// No model, no candidates, or an answer that does not parse: it adds, exactly
// as Add would. A reconciliation that cannot run must not lose the memory.
func (s *Service) AddReconciled(ctx context.Context, mem *domain.Memory) (domain.MemoryReconcileOutcome, error) {
	if mem == nil {
		return domain.MemoryReconcileOutcome{}, fmt.Errorf("memory is nil")
	}
	add := func() (domain.MemoryReconcileOutcome, error) {
		if err := s.Add(ctx, mem); err != nil {
			return domain.MemoryReconcileOutcome{}, err
		}
		return domain.MemoryReconcileOutcome{Op: domain.MemoryOpAdd, ID: mem.ID}, nil
	}
	if s == nil || s.llm == nil || strings.TrimSpace(mem.Content) == "" {
		return add()
	}

	candidates := s.reconcileCandidates(ctx, explicitStoreRequest(mem))
	if len(candidates) == 0 {
		return add()
	}

	verdict, ok := s.askExplicitReconcile(ctx, mem.Content, candidates)
	if !ok {
		return add()
	}
	known := map[string]bool{}
	for _, c := range candidates {
		known[c.ID] = true
	}
	switch verdict.Op {
	case domain.MemoryOpNoop:
		if known[verdict.TargetID] {
			return domain.MemoryReconcileOutcome{Op: domain.MemoryOpNoop, TargetID: verdict.TargetID}, nil
		}
	case domain.MemoryOpUpdate:
		if known[verdict.TargetID] {
			if err := s.Add(ctx, mem); err != nil {
				return domain.MemoryReconcileOutcome{}, err
			}
			s.supersedeOrKeep(ctx, verdict.TargetID, mem.ID)
			return domain.MemoryReconcileOutcome{Op: domain.MemoryOpUpdate, TargetID: verdict.TargetID, ID: mem.ID}, nil
		}
	}
	// add, or a verdict pointing at a memory the model was not shown.
	return add()
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
}

func (s *Service) askExplicitReconcile(ctx context.Context, content string, candidates []*domain.Memory) (explicitVerdict, bool) {
	var b strings.Builder
	b.WriteString("A memory is about to be saved:\n")
	b.WriteString(oneLine(content))
	b.WriteString("\n\nMemories already stored, each with its id:\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "- [%s] %s\n", c.ID, oneLine(c.Content))
	}
	b.WriteString(`
Decide one op:
- "noop": a stored memory already says the same thing; set target_id to it. Nothing is saved.
- "update": the new memory replaces a stored one that is now out of date (the same attribute of the same subject with a new value); set target_id to the replaced memory. The new memory is saved and the old one retired, so the new content must stand on its own.
- "add": neither; the new memory is saved beside the others. Leave target_id empty.
Judge by meaning, in whatever language the memories are written.`)

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"op":        map[string]interface{}{"type": "string", "enum": []string{domain.MemoryOpAdd, domain.MemoryOpUpdate, domain.MemoryOpNoop}},
			"target_id": map[string]interface{}{"type": "string"},
		},
		"required": []string{"op", "target_id"},
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
