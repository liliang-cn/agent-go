package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
)

// Write-time reconciliation.
//
// Before this, every item the extraction call returned was added. "I live in
// Berlin" and, a week later, "I moved to Vienna" both stayed current forever,
// and a later "where do I live?" was answered from whichever ranked first.
// Measured on the scripted moved-city scenario: three live residence rows and
// Berlin injected three times into the prompt that asked.
//
// The fix rides on the extraction call the writer already makes, exactly as
// pkg/timeaware does: the most similar existing memories in the request's
// scope chain are shown in the same prompt, and each extracted item comes
// back with a verdict — add, update (replaces target_id) or noop (target_id
// already says it). Zero extra model calls. Nothing here reads the user's
// wording to decide anything; the model decides, and the runtime only checks
// that a target it names is one it was actually shown.

// reconcileCandidateLimit is how many existing memories the extraction call
// is shown. Enough for the common "this changed" case, small enough that the
// prompt does not grow with the store.
const reconcileCandidateLimit = 5

// reconcileListWindow bounds the recency fallback's List call.
const reconcileListWindow = 200

// reconcileCandidates returns up to reconcileCandidateLimit current memories
// in the request's scope chain, most similar to the interaction first.
//
// Similarity is whatever the backend can do: vector search when an embedder
// is configured, its own text search otherwise. A lexical ranker cannot see
// that "moved to Vienna" is about "lives in Berlin" — they share no word — so
// the list is topped up with the most recent memories in scope. That is not a
// guess about meaning; it is showing the model the memories most likely to be
// the ones this conversation is revising, and letting it judge.
func (s *Service) reconcileCandidates(ctx context.Context, req *domain.MemoryStoreRequest) []*domain.Memory {
	if s == nil || s.store == nil || req == nil {
		return nil
	}
	scopes := DefaultScopeChain(req.SessionID, req.AgentID, req.TeamID, req.UserID).ToSlice()
	query := strings.TrimSpace(req.TaskGoal + "\n" + req.TaskResult)

	seen := map[string]struct{}{}
	var out []*domain.Memory
	take := func(m *domain.Memory) {
		if m == nil || strings.TrimSpace(m.ID) == "" || strings.HasPrefix(m.ID, "ent_") {
			return
		}
		if _, dup := seen[m.ID]; dup || domain.MemoryIsSuperseded(m) || m.Archived {
			return
		}
		if !memoryMatchesAnyScope(m, scopes) {
			return
		}
		seen[m.ID] = struct{}{}
		out = append(out, m)
	}

	var ranked []*domain.MemoryWithScore
	if query != "" {
		if vec, ok := s.embed(ctx, query); ok {
			ranked, _ = s.store.SearchByScope(ctx, vec, scopes, reconcileCandidateLimit*2)
		}
	}
	if len(ranked) == 0 && query != "" {
		ranked, _ = s.store.SearchByText(ctx, query, reconcileCandidateLimit*2)
	}
	for _, r := range ranked {
		if r != nil {
			take(r.Memory)
		}
		if len(out) >= reconcileCandidateLimit {
			return out
		}
	}

	// Top up with the most recent current memories in scope. An error here
	// (a backend that cannot List) just means fewer candidates.
	recent, _, err := s.store.List(ctx, reconcileListWindow, 0)
	if err != nil {
		return out
	}
	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].CreatedAt.After(recent[j].CreatedAt)
	})
	for _, m := range recent {
		if len(out) >= reconcileCandidateLimit {
			break
		}
		take(m)
	}
	return out
}

// reconcilePromptRules is appended to the extraction prompt. It names every
// candidate by id so the model can point at one, and says what each verdict
// does, including the one people get wrong: an update retires the old
// memory, so the new content must stand on its own.
func reconcilePromptRules(candidates []*domain.Memory) string {
	var sb strings.Builder
	sb.WriteString("\n\nExisting memories (reconcile every extracted item against these):\n")
	if len(candidates) == 0 {
		sb.WriteString("(none — every item is new; use op \"add\")\n")
	}
	for _, m := range candidates {
		kind := string(domain.MemoryKindOf(m))
		if kind == "" {
			kind = "-"
		}
		fmt.Fprintf(&sb, "- id=%s [%s, %s] (written %s): %s\n",
			m.ID, m.Type, kind, m.CreatedAt.Format("2006-01-02"), oneLine(m.Content))
	}
	sb.WriteString(`
For each extracted item set "op":
- "add": information none of the existing memories covers.
- "update": the item replaces an existing memory that is no longer true (it changed, or was corrected). Set "target_id" to that memory's id. The old memory stops being shown, so write the item's content so it stands on its own — and when the old memory also says other things that are still true, restate them in the item: whatever the replacement leaves out is forgotten.
- "noop": an existing memory already says this. Set "target_id" to it; nothing is stored.
Only use ids from the list above.

For each extracted item set "kind":
- "world": an objective fact about the world or the person's circumstances.
- "experience": something that happened, or what was done and learned doing it.
- "opinion": a subjective judgment or preference someone holds.
- "observation": a summary drawn from several other memories.`)
	return sb.String()
}

// reconcileSchemaFields are grafted onto the extraction schema's item.
func reconcileSchemaFields() map[string]interface{} {
	kinds := make([]string, 0, len(domain.MemoryKinds))
	for _, k := range domain.MemoryKinds {
		kinds = append(kinds, string(k))
	}
	return map[string]interface{}{
		"op":        map[string]interface{}{"type": "string", "enum": []string{domain.MemoryOpAdd, domain.MemoryOpUpdate, domain.MemoryOpNoop}},
		"target_id": map[string]interface{}{"type": "string", "description": "id of the existing memory an update replaces or a noop repeats; empty for add"},
		"kind":      map[string]interface{}{"type": "string", "enum": kinds},
	}
}

// reconcileRequiredFields are required, not optional, for the reason
// timeaware.RequiredFields gives: a model working through a long extraction
// prompt skips optional fields, and a skipped op reads exactly like "add".
func reconcileRequiredFields() []string { return []string{"op", "kind"} }

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Supersede records that oldID has been replaced by newID. The old memory is
// kept — it was true once — but stops being injected.
//
// It needs a backend that implements domain.MemoryStaleMarker; one that does
// not gets domain.ErrMemoryStoreUnsupported back rather than a pretend
// success, because a mark nobody persisted means the old fact goes on being
// injected as current.
func (s *Service) Supersede(ctx context.Context, oldID, newID string) error {
	if s == nil || s.store == nil {
		return domain.ErrMemoryStoreUnsupported
	}
	marker, ok := s.store.(domain.MemoryStaleMarker)
	if !ok {
		return domain.ErrMemoryStoreUnsupported
	}
	if err := marker.MarkStale(ctx, oldID, newID); err != nil {
		return err
	}
	// The old memory's edges were true of the old fact; they go with it.
	s.dropMemoryGraph(ctx, oldID)
	if s.shadowIndex != nil && s.shadowIndex != s.store {
		if shadow, ok := s.shadowIndex.(domain.MemoryStaleMarker); ok {
			_ = shadow.MarkStale(ctx, oldID, newID)
		}
	}
	if s.navigator != nil {
		s.navigator.InvalidateCache()
	}
	return nil
}

// supersedeOrKeep is Supersede for the write path, where a backend that
// cannot mark is not an error: the new memory is already stored, the old one
// stays current, and the log says so.
func (s *Service) supersedeOrKeep(ctx context.Context, oldID, newID string) {
	if err := s.Supersede(ctx, oldID, newID); err != nil {
		agentgolog.WithModule("memory.autostore").Warn("could not supersede the memory an update replaces; both stay current",
			"old_id", oldID, "new_id", newID, "error", err)
	}
}

// dropSuperseded removes memories that have been replaced. Applied on the
// read path whatever backend answered, so a superseded memory is never
// injected as current.
func dropSuperseded(memories []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	if len(memories) == 0 {
		return memories
	}
	out := memories[:0:0]
	for _, m := range memories {
		if m == nil || m.Memory == nil || domain.MemoryIsSuperseded(m.Memory) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// memoryLabel is the bracketed tag a memory carries when injected: its type,
// and its kind when one was recorded.
func memoryLabel(m *domain.Memory) string {
	if m == nil {
		return ""
	}
	if kind := domain.MemoryKindOf(m); kind != "" && string(kind) != string(m.Type) {
		return fmt.Sprintf("%s, %s", m.Type, kind)
	}
	return string(m.Type)
}
