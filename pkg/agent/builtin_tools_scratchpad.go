package agent

import (
	"context"
	"fmt"
	"sync"
)

// Built-in scratchpad tools: a tiny in-memory todo/plan store so long-horizon
// agents can keep track of a multi-step plan across many tool rounds without
// losing the thread. Lists are keyed by an arbitrary string (args.key, default
// "default"). Mirrors the RegisterFetchURLTool registration pattern.

// toolArgStringSlice extracts a string array tool argument, tolerating every
// shape it can arrive in: []interface{} (JSON tool calls), []string (exported
// from the PTC Goja sandbox), or a single string. Returns ok=false only when
// the key is missing or an unusable type.
func toolArgStringSlice(args map[string]interface{}, key string) ([]string, bool) {
	switch v := args[key].(type) {
	case []string:
		return v, true
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, e := range v {
			out = append(out, fmt.Sprintf("%v", e))
		}
		return out, true
	case string:
		if v == "" {
			return nil, false
		}
		return []string{v}, true
	default:
		return nil, false
	}
}

type scratchpadItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
	// Note is what the step produced. See PlanItem.
	Note string `json:"note,omitempty"`
}

// scratchpadManager holds one service's plan lists.
//
// One per Service, not one per process. It used to be a package-level var, so
// two tasks running in the same process shared a namespace and quietly
// overwrote each other's plans whenever they picked the same key — which they
// do, because the default key is "default".
type scratchpadManager struct {
	mu    sync.RWMutex
	lists map[string][]scratchpadItem
	// store, when set, makes the plan outlive the process. nil is the default
	// and keeps the original in-memory behaviour exactly.
	store PlanStore
	// loaded records which keys have been pulled in from the store, so a key
	// the agent deliberately emptied does not refill itself on the next read.
	loaded map[string]bool
}

func newScratchpadManager(store PlanStore) *scratchpadManager {
	return &scratchpadManager{
		lists:  make(map[string][]scratchpadItem),
		store:  store,
		loaded: make(map[string]bool),
	}
}

func (m *scratchpadManager) set(key string, items []string) []scratchpadItem {
	list := make([]scratchpadItem, 0, len(items))
	for _, t := range items {
		list = append(list, scratchpadItem{Text: t})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Marked loaded without reading: the caller is replacing the plan outright,
	// so fetching the one it is about to discard would only risk resurrecting
	// it if the write then failed.
	if m.loaded == nil {
		m.loaded = map[string]bool{}
	}
	m.loaded[key] = true
	m.lists[key] = list
	m.savePlan(key, list)
	return list
}

func (m *scratchpadManager) add(key, text string) []scratchpadItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	m.lists[key] = append(m.lists[key], scratchpadItem{Text: text})
	m.savePlan(key, m.lists[key])
	return append([]scratchpadItem(nil), m.lists[key]...)
}

func (m *scratchpadManager) check(key string, index int, note string) ([]scratchpadItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	list := m.lists[key]
	if index < 0 || index >= len(list) {
		return nil, fmt.Errorf("index %d out of range (list has %d items)", index, len(list))
	}
	list[index].Done = true
	if note != "" {
		// Only overwritten when something was said. Re-checking a step to
		// correct its index should not silently erase what it produced.
		list[index].Note = note
	}
	m.savePlan(key, list)
	return append([]scratchpadItem(nil), list...), nil
}

func (m *scratchpadManager) get(key string) []scratchpadItem {
	// A write lock even though this reads: the first touch of a key may have to
	// pull it in from the store.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	return append([]scratchpadItem(nil), m.lists[key]...)
}

// scratchpadDefaultKey is the list a plan lands in when the model does not
// name one, which is most of the time.
const scratchpadDefaultKey = "default"

func scratchpadKey(args map[string]interface{}) string {
	if k := toolArgString(args, "key"); k != "" {
		return k
	}
	return scratchpadDefaultKey
}

func scratchpadItemsPayload(list []scratchpadItem) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for i, it := range list {
		item := map[string]interface{}{"index": i, "text": it.Text, "done": it.Done}
		if it.Note != "" {
			item["note"] = it.Note
		}
		out = append(out, item)
	}
	return out
}

// RegisterScratchpadTools registers the in-memory todo/plan tools on a service.
// No-op if svc is nil.
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterScratchpadTools(svc)
func RegisterScratchpadTools(svc *Service) {
	if svc == nil {
		return
	}
	has := func(name string) bool {
		return svc.toolRegistry != nil && svc.toolRegistry.Has(name)
	}
	pad := svc.scratchpadStore()
	destMeta := ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}
	roMeta := ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}

	// --- scratchpad_set ---
	if !has("scratchpad_set") {
		svc.AddToolWithMetadata(
			"scratchpad_set",
			"Replace the whole plan list with a set of todo items (items is an array of strings). Use it to write down the plan when starting a multi-step task. Optional key selects one of several lists.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Array of todo item texts"},
				},
				"required": []string{"items"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				items, ok := toolArgStringSlice(args, "items")
				if !ok {
					return toolErr("items must be an array of strings"), nil
				}
				list := pad.set(scratchpadKey(args), items)
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_add ---
	if !has("scratchpad_add") {
		svc.AddToolWithMetadata(
			"scratchpad_add",
			"Append one todo item to the plan list. Optional key.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":  map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"text": map[string]interface{}{"type": "string", "description": "Todo item text"},
				},
				"required": []string{"text"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				text := toolArgString(args, "text")
				if text == "" {
					return toolErr("text required"), nil
				}
				list := pad.add(scratchpadKey(args), text)
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_check ---
	if !has("scratchpad_check") {
		svc.AddToolWithMetadata(
			"scratchpad_check",
			"Mark the todo item at position index (0-based) as done. Pass note to record what the step produced — that note is what lets this task be picked up later without redoing the work. Optional key.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"index": map[string]interface{}{"type": "integer", "description": "Index of the todo item to mark done (0-based)"},
					"note":  map[string]interface{}{"type": "string", "description": "What this step produced or concluded — the port you found, the file you wrote, the approach you ruled out. Recorded so the work is not repeated if this task is resumed later."},
				},
				"required": []string{"index"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				list, err := pad.check(scratchpadKey(args), toolArgInt(args, "index"), toolArgString(args, "note"))
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_get ---
	if !has("scratchpad_get") {
		svc.AddToolWithMetadata(
			"scratchpad_get",
			"Read the plan list and return the todo items with their index and done flag. Optional key.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key": map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				list := pad.get(scratchpadKey(args))
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			roMeta,
		)
	}
}
