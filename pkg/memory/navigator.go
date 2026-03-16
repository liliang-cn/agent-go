package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/store"
)

const navigatorCacheTTL = 5 * time.Minute

type cacheEntry struct {
	ids       []string
	reasoning string // LLM's explanation of why these IDs were selected
	expiresAt time.Time
}

// NavigateResult holds the selected memories and the LLM's selection reasoning.
type NavigateResult struct {
	Memories  []*domain.Memory
	Reasoning string // PageIndex-style explanation: "Selected because..."
}

// IndexNavigator implements PageIndex-style memory retrieval for FileMemoryStore.
// Instead of vector similarity, it lets the LLM read the memory index (_index/)
// and reason about which memories are relevant to a query.
type IndexNavigator struct {
	fileStore *store.FileMemoryStore
	llm       domain.Generator

	mu    sync.RWMutex
	cache map[string]cacheEntry // query → selected IDs cache
}

// NewIndexNavigator creates a new IndexNavigator.
func NewIndexNavigator(fileStore *store.FileMemoryStore, llm domain.Generator) *IndexNavigator {
	return &IndexNavigator{
		fileStore: fileStore,
		llm:       llm,
		cache:     make(map[string]cacheEntry),
	}
}

// InvalidateCache clears all cached navigation results.
// Should be called when new memories are stored.
func (n *IndexNavigator) InvalidateCache() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cache = make(map[string]cacheEntry)
}

// Navigate uses the LLM to read the memory index and select the most relevant memories.
// Results are cached by query string for navigatorCacheTTL.
// Deprecated: use NavigateWithReason to also capture the LLM's selection logic.
func (n *IndexNavigator) Navigate(ctx context.Context, query string, topK int) ([]*domain.Memory, error) {
	r, err := n.NavigateWithScopesAndReason(ctx, query, nil, topK)
	if err != nil || r == nil {
		return nil, err
	}
	return r.Memories, nil
}

// NavigateWithReason returns both selected memories and the LLM's reasoning string.
// The reasoning can be surfaced as MemoryLogic in ExecutionResult for transparency.
func (n *IndexNavigator) NavigateWithReason(ctx context.Context, query string, topK int) (*NavigateResult, error) {
	return n.NavigateWithScopesAndReason(ctx, query, nil, topK)
}

// NavigateWithScopesAndReason returns both selected memories and the LLM's reasoning string
// using an explicit scope chain. Earlier scopes have higher priority.
func (n *IndexNavigator) NavigateWithScopesAndReason(ctx context.Context, query string, scopes []domain.MemoryScope, topK int) (*NavigateResult, error) {
	if n.llm == nil || n.fileStore == nil {
		return nil, nil
	}

	// 1. Check cache
	cacheKey := navigatorCacheKey(query, scopes)
	ids, reasoning, cached := n.cachedEntry(cacheKey)
	if !cached {
		// 2. Read the memory indexes (TOC) according to the scope chain.
		promptIndex, err := n.buildPromptIndex(ctx, scopes)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(promptIndex) == "" {
			return nil, nil
		}

		// 3. Ask the LLM to select relevant IDs.
		promptText := fmt.Sprintf(`You are a memory retrieval assistant. Given a user query and a memory index, select the IDs of the most relevant memories.

User query: %s

%s
Instructions:
- Select at most %d memory IDs that are most relevant to the query.
- Prefer observations over raw facts when observations cover the topic.
- Prefer earlier scopes in the scope chain when relevance is similar.
- Use logical reasoning, not just keyword matching.
- If no memories are relevant, return an empty list.
- Provide a brief "reasoning" string explaining your selection.

Output valid JSON only:
{"ids": ["id1", "id2"], "reasoning": "Selected because..."}`, query, promptIndex, topK)

		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"ids": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
				"reasoning": map[string]interface{}{"type": "string"},
			},
			"required": []string{"ids"},
		}

		result, err := n.llm.GenerateStructured(ctx, promptText, schema, &domain.GenerationOptions{Temperature: 0.1})
		if err != nil {
			return nil, fmt.Errorf("navigator LLM call failed: %w", err)
		}

		// 4. Parse selected IDs + reasoning
		var selected struct {
			IDs       []string `json:"ids"`
			Reasoning string   `json:"reasoning"`
		}
		raw := strings.TrimSpace(result.Raw)
		if strings.HasPrefix(raw, "```") {
			lines := strings.SplitN(raw, "\n", 2)
			if len(lines) == 2 {
				raw = lines[1]
			}
			raw = strings.TrimSuffix(raw, "```")
			raw = strings.TrimSpace(raw)
		}
		if err := json.Unmarshal([]byte(raw), &selected); err != nil {
			return nil, fmt.Errorf("failed to parse navigator result: %w", err)
		}
		ids = selected.IDs
		reasoning = selected.Reasoning
		n.setCachedEntry(cacheKey, ids, reasoning)
	}

	// 5. Fetch full memory content for each selected ID
	var memories []*domain.Memory
	for _, id := range ids {
		m, err := n.fileStore.Get(ctx, id)
		if err != nil {
			continue
		}
		memories = append(memories, m)
	}

	return &NavigateResult{Memories: memories, Reasoning: reasoning}, nil
}

func (n *IndexNavigator) buildPromptIndex(ctx context.Context, scopes []domain.MemoryScope) (string, error) {
	if len(scopes) == 0 {
		return n.buildMergedPromptIndex(ctx)
	}

	var sb strings.Builder
	sb.WriteString("Memory Scope Chain (highest priority first):\n\n")
	sectionCount := 0
	for _, scope := range scopes {
		idx, err := n.fileStore.ReadScopeIndex(ctx, scope)
		if err != nil || idx == nil || len(idx.Entries) == 0 {
			continue
		}

		scopeLabel := ScopeString(scope)
		sb.WriteString("## Scope: ")
		sb.WriteString(scopeLabel)
		sb.WriteString("\n")
		appendIndexEntries(&sb, idx.Entries)
		sb.WriteString("\n")
		sectionCount++
	}
	if sectionCount == 0 {
		return n.buildMergedPromptIndex(ctx)
	}
	return sb.String(), nil
}

func (n *IndexNavigator) buildMergedPromptIndex(ctx context.Context) (string, error) {
	idx, err := n.fileStore.ReadIndex(ctx)
	if err != nil || idx == nil || len(idx.Entries) == 0 {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Memory Index (observations first, then facts; format: [id] importance | summary):\n\n")
	appendIndexEntries(&sb, idx.Entries)
	return sb.String(), nil
}

func appendIndexEntries(sb *strings.Builder, entries []store.MemoryIndexEntry) {
	for _, e := range entries {
		if e.IsStale || e.Archived || e.Type != domain.MemoryTypeObservation {
			continue
		}
		sb.WriteString(fmt.Sprintf("- [%s][obs] %.2f | %s\n", e.ID, e.Importance, e.Summary))
	}
	for _, e := range entries {
		if e.IsStale || e.Archived || e.Type == domain.MemoryTypeObservation {
			continue
		}
		sb.WriteString(fmt.Sprintf("- [%s][%s] %.2f | %s\n", e.ID, e.Type, e.Importance, e.Summary))
	}
}

func navigatorCacheKey(query string, scopes []domain.MemoryScope) string {
	if len(scopes) == 0 {
		return query
	}

	var sb strings.Builder
	sb.WriteString(query)
	sb.WriteString("||")
	for _, scope := range scopes {
		sb.WriteString(ScopeString(scope))
		sb.WriteString("|")
	}
	return sb.String()
}

func (n *IndexNavigator) cachedEntry(query string) (ids []string, reasoning string, ok bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	entry, exists := n.cache[query]
	if !exists || time.Now().After(entry.expiresAt) {
		return nil, "", false
	}
	return entry.ids, entry.reasoning, true
}

// cachedIDs is kept for backward compatibility with existing callers.
func (n *IndexNavigator) cachedIDs(query string) ([]string, bool) {
	ids, _, ok := n.cachedEntry(query)
	return ids, ok
}

func (n *IndexNavigator) setCachedEntry(query string, ids []string, reasoning string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cache[query] = cacheEntry{ids: ids, reasoning: reasoning, expiresAt: time.Now().Add(navigatorCacheTTL)}
}

func (n *IndexNavigator) setCachedIDs(query string, ids []string) {
	n.setCachedEntry(query, ids, "")
}
