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
			"用一组待办项整体替换计划清单(items 为字符串数组)。用于在开始一个多步任务时写下计划。可选 key 区分多份清单。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "清单标识,默认 default"},
					"items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "待办项文本数组"},
				},
				"required": []string{"items"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				raw, ok := args["items"].([]interface{})
				if !ok {
					return toolErr("items must be an array of strings"), nil
				}
				items := make([]string, 0, len(raw))
				for _, v := range raw {
					items = append(items, fmt.Sprintf("%v", v))
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
			"向计划清单追加一个待办项。可选 key。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":  map[string]interface{}{"type": "string", "description": "清单标识,默认 default"},
					"text": map[string]interface{}{"type": "string", "description": "待办项文本"},
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
			"把清单中第 index 个待办项(0基)标记为已完成。可选 key。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "清单标识,默认 default"},
					"index": map[string]interface{}{"type": "integer", "description": "要标记完成的待办项下标(0基)"},
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
			"读取计划清单,返回带下标与完成标记的待办项列表。可选 key。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key": map[string]interface{}{"type": "string", "description": "清单标识,默认 default"},
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
