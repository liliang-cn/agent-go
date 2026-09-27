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
// package can only ever partly know. So an operator states it, a small bundled
// table covers the names someone happened to add, and "I do not know this
// model's window" is a value a caller reads — never a guess that looks like an
// answer.

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
// Registrations win over the bundled table. Registering the same pattern twice
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

// fallbackWindows is the bundled table: published windows for model families
// whose numbers are stable and well known. It is deliberately short. A wrong
// window is worse than an unknown one — too large and compaction fires after
// the provider has already rejected the request — so anything not listed here
// is reported unknown and the caller falls back to its fixed default.
var fallbackWindows = map[string]ModelWindow{
	"gpt-3.5-turbo":     {ContextTokens: 16385, MaxOutputTokens: 4096},
	"gpt-4":             {ContextTokens: 8192, MaxOutputTokens: 4096},
	"gpt-4-turbo":       {ContextTokens: 128000, MaxOutputTokens: 4096},
	"gpt-4o":            {ContextTokens: 128000, MaxOutputTokens: 16384},
	"gpt-4.1":           {ContextTokens: 1047576, MaxOutputTokens: 32768},
	"claude-3-opus":     {ContextTokens: 200000, MaxOutputTokens: 4096},
	"claude-3-sonnet":   {ContextTokens: 200000, MaxOutputTokens: 4096},
	"claude-3-haiku":    {ContextTokens: 200000, MaxOutputTokens: 4096},
	"claude-3.5-sonnet": {ContextTokens: 200000, MaxOutputTokens: 8192},
	"gemini-1.5-pro":    {ContextTokens: 2097152, MaxOutputTokens: 8192},
	"gemini-1.5-flash":  {ContextTokens: 1048576, MaxOutputTokens: 8192},
	"gemini-2.5":        {ContextTokens: 1048576, MaxOutputTokens: 65536},
}

// LookupModelWindow resolves a model's context window, reporting whether any
// source knew it. Registrations are consulted before the bundled table, and
// within each the longest matching pattern wins so "gpt-4o" beats "gpt-4".
func LookupModelWindow(model string) (ModelWindow, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return ModelWindow{}, false
	}

	windowMu.RLock()
	registered := make(map[string]ModelWindow, len(registeredWindow))
	for k, v := range registeredWindow {
		registered[k] = v
	}
	windowMu.RUnlock()

	for _, table := range []map[string]ModelWindow{registered, fallbackWindows} {
		keys := make([]string, 0, len(table))
		for k := range table {
			keys = append(keys, k)
		}
		// Longest first; ties broken by name so the answer never depends on
		// map order.
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) > len(keys[j])
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			if strings.Contains(name, k) {
				return table[k], true
			}
		}
	}
	return ModelWindow{}, false
}
