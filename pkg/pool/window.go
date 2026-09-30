package pool

import (
	"sort"
	"strings"
	"sync"
)

// Model context windows.
//
// Same shape as pricing, for the same reason. The compaction threshold used to
// be one number for every model — 60000 tokens — which is a third of a 200k
// window and more than a 32k window has. The window is what the threshold
// should be derived from, and the window is a fact about a model that this
// package can only ever partly know. So an operator states it, and "I do not
// know this model's window" is a value a caller reads — never a guess that
// looks like an answer. A bundled table was tried and went stale: it knew
// gpt-3.5 and claude-3 and nothing past gemini-2.5.

// ModelWindow is what one model can hold.
type ModelWindow struct {
	// ContextTokens is the whole window: prompt plus completion.
	ContextTokens int
	// MaxOutputTokens is the most a single response may use. Zero means
	// "not stated"; the caller then reserves its own per-turn output budget.
	MaxOutputTokens int
}

var (
	windowMu         sync.RWMutex
	registeredWindow = map[string]ModelWindow{}
)

// RegisterModelWindow states the context window for every model whose name
// contains pattern (matched case-insensitively, longest pattern first).
// Registering the same pattern twice
// replaces it; an empty pattern or a non-positive window is ignored.
func RegisterModelWindow(pattern string, w ModelWindow) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" || w.ContextTokens <= 0 {
		return
	}
	windowMu.Lock()
	defer windowMu.Unlock()
	registeredWindow[pattern] = w
}

// UnregisterModelWindow removes a registration.
func UnregisterModelWindow(pattern string) {
	windowMu.Lock()
	defer windowMu.Unlock()
	delete(registeredWindow, strings.ToLower(strings.TrimSpace(pattern)))
}

// LookupModelWindow resolves a model's context window, reporting whether any
// registration knew it. The longest matching pattern wins, so "gpt-4o" beats
// "gpt-4".
func LookupModelWindow(model string) (ModelWindow, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return ModelWindow{}, false
	}

	windowMu.RLock()
	defer windowMu.RUnlock()
	keys := make([]string, 0, len(registeredWindow))
	for k := range registeredWindow {
		keys = append(keys, k)
	}
	// Longest first; ties broken by name so the answer never depends on map
	// order.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		if strings.Contains(name, k) {
			return registeredWindow[k], true
		}
	}
	return ModelWindow{}, false
}
