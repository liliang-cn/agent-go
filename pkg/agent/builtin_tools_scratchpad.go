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
}

type scratchpadManager struct {
	mu    sync.RWMutex
	lists map[string][]scratchpadItem
}

var globalScratchpad = &scratchpadManager{
	lists: make(map[string][]scratchpadItem),
}

func (m *scratchpadManager) set(key string, items []string) []scratchpadItem {
	list := make([]scratchpadItem, 0, len(items))
	for _, t := range items {
		list = append(list, scratchpadItem{Text: t})
	}
	m.mu.Lock()
	m.lists[key] = list
	m.mu.Unlock()
	return list
}

func (m *scratchpadManager) add(key, text string) []scratchpadItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lists[key] = append(m.lists[key], scratchpadItem{Text: text})
	return append([]scratchpadItem(nil), m.lists[key]...)
}

func (m *scratchpadManager) check(key string, index int) ([]scratchpadItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.lists[key]
	if index < 0 || index >= len(list) {
		return nil, fmt.Errorf("index %d out of range (list has %d items)", index, len(list))
	}
	list[index].Done = true
	return append([]scratchpadItem(nil), list...), nil
}

func (m *scratchpadManager) get(key string) []scratchpadItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]scratchpadItem(nil), m.lists[key]...)
}

func scratchpadKey(args map[string]interface{}) string {
	if k := toolArgString(args, "key"); k != "" {
		return k
	}
	return "default"
}

func scratchpadItemsPayload(list []scratchpadItem) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for i, it := range list {
		out = append(out, map[string]interface{}{"index": i, "text": it.Text, "done": it.Done})
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
				list := globalScratchpad.set(scratchpadKey(args), items)
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
				list := globalScratchpad.add(scratchpadKey(args), text)
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_check ---
	if !has("scratchpad_check") {
		svc.AddToolWithMetadata(
			"scratchpad_check",
			"Mark the todo item at position index (0-based) as done. Optional key.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"index": map[string]interface{}{"type": "integer", "description": "Index of the todo item to mark done (0-based)"},
				},
				"required": []string{"index"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				list, err := globalScratchpad.check(scratchpadKey(args), toolArgInt(args, "index"))
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
				list := globalScratchpad.get(scratchpadKey(args))
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			roMeta,
		)
	}
}
