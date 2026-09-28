package agent

import (
	"context"
	"fmt"
	"strings"
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
	// ID and After make the plan a graph. See PlanItem and plan_dag.go.
	ID    string   `json:"id,omitempty"`
	After []string `json:"after,omitempty"`
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

// setSteps replaces the plan with steps that may name IDs and predecessors.
// A plan with a cycle, a duplicate ID or a reference to no step is refused
// and the stored plan is left as it was.
func (m *scratchpadManager) setSteps(key string, steps []scratchpadItem) ([]scratchpadItem, error) {
	list := cloneScratchpadItems(steps)
	for i := range list {
		list[i].Done = false
		list[i].Note = ""
	}
	if err := validatePlanGraph(list); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded == nil {
		m.loaded = map[string]bool{}
	}
	m.loaded[key] = true
	m.lists[key] = list
	m.savePlan(key, list)
	return cloneScratchpadItems(list), nil
}

func (m *scratchpadManager) add(key, text string) []scratchpadItem {
	list, _ := m.addStep(key, scratchpadItem{Text: text})
	return list
}

// addStep appends one step, refusing it when its ID or predecessors would
// leave the plan something other than a DAG.
func (m *scratchpadManager) addStep(key string, step scratchpadItem) ([]scratchpadItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	step.Done, step.Note = false, ""
	step.After = cloneStrings(step.After)
	next := append(cloneScratchpadItems(m.lists[key]), step)
	if err := validatePlanGraph(next); err != nil {
		return nil, err
	}
	m.lists[key] = next
	m.savePlan(key, m.lists[key])
	return cloneScratchpadItems(m.lists[key]), nil
}

func (m *scratchpadManager) check(key string, index int, note string) ([]scratchpadItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	list := m.lists[key]
	if index < 0 || index >= len(list) {
		return nil, fmt.Errorf("index %d out of range (list has %d items)", index, len(list))
	}
	if refusal := planCheckRefusal(list, index); refusal != nil {
		return nil, refusal
	}
	list[index].Done = true
	if note != "" {
		// Only overwritten when something was said. Re-checking a step to
		// correct its index should not silently erase what it produced.
		list[index].Note = note
	}
	m.savePlan(key, list)
	return cloneScratchpadItems(list), nil
}

// note records what a step is in the middle of, without claiming it is done.
//
// It exists because a long task hands over at segment boundaries, and the
// hand-off could only carry finished work. A step that took a whole segment
// and did not finish reached the next segment as a bare unchecked line: what
// was tried, what nearly worked, what was ruled out — all of it gone, and the
// next segment starts the same investigation from the top. Two segments of a
// soak run went the same way on the same milestone before this existed.
func (m *scratchpadManager) note(key string, index int, note string) ([]scratchpadItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	list := m.lists[key]
	if index < 0 || index >= len(list) {
		return nil, fmt.Errorf("index %d out of range (list has %d items)", index, len(list))
	}
	list[index].Note = note
	m.savePlan(key, list)
	return cloneScratchpadItems(list), nil
}

func (m *scratchpadManager) get(key string) []scratchpadItem {
	// A write lock even though this reads: the first touch of a key may have to
	// pull it in from the store.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded(key)
	return cloneScratchpadItems(m.lists[key])
}

// scratchpadDefaultKey is the list a plan lands in when the model does not
// name one, which is most of the time.
const scratchpadDefaultKey = "default"

func scratchpadKey(ctx context.Context, args map[string]interface{}) string {
	if k := toolArgString(args, "key"); k != "" {
		return k
	}
	// The run may have been told where its plan lives (WithPlanKey); a model
	// that does not name a list is asking for that one.
	if k := currentPlanKey(ctx); k != "" {
		return k
	}
	return scratchpadDefaultKey
}

func scratchpadItemsPayload(list []scratchpadItem) []map[string]interface{} {
	// ready is only reported for a plan that states an order; a flat plan's
	// payload is what it always was.
	var ready map[int]bool
	if planHasDependencies(list) {
		ready = map[int]bool{}
		for _, i := range planReadySteps(list) {
			ready[i] = true
		}
	}
	out := make([]map[string]interface{}, 0, len(list))
	for i, it := range list {
		item := map[string]interface{}{"index": i, "text": it.Text, "done": it.Done}
		if it.Note != "" {
			item["note"] = it.Note
		}
		if it.ID != "" {
			item["id"] = it.ID
		}
		if len(it.After) > 0 {
			item["after"] = append([]string(nil), it.After...)
		}
		if ready != nil && !it.Done {
			item["ready"] = ready[i]
		}
		out = append(out, item)
	}
	return out
}

// scratchpadToolError renders a refusal: a plan-graph refusal keeps its
// structure, anything else is a plain message.
func scratchpadToolError(err error) map[string]interface{} {
	if pg, ok := err.(*planGraphError); ok {
		return pg.payload()
	}
	return toolErr(err.Error())
}

// toolArgPlanSteps reads scratchpad_set's steps argument: an array of
// {id, text, after} objects.
func toolArgPlanSteps(args map[string]interface{}) ([]scratchpadItem, error) {
	var raw []interface{}
	switch v := args["steps"].(type) {
	case []interface{}:
		raw = v
	case []map[string]interface{}:
		for _, m := range v {
			raw = append(raw, m)
		}
	default:
		return nil, fmt.Errorf("steps must be an array of {id, text, after} objects")
	}
	out := make([]scratchpadItem, 0, len(raw))
	for i, e := range raw {
		obj, ok := e.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("steps[%d] must be an object with text, and optionally id and after", i)
		}
		step := scratchpadItem{
			Text: strings.TrimSpace(toolArgString(obj, "text")),
			ID:   strings.TrimSpace(toolArgString(obj, "id")),
		}
		if step.Text == "" {
			return nil, fmt.Errorf("steps[%d] needs text", i)
		}
		if after, ok := toolArgStringSlice(obj, "after"); ok {
			step.After = trimNonEmpty(after)
		}
		out = append(out, step)
	}
	return out, nil
}

func trimNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
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
			"Replace the whole plan list. Pass items (an array of strings) for a plain checklist, or steps (objects with id, text and after) when some steps can only happen after others; steps with no after can be done in any order. Use it to write down the plan when starting a multi-step task. Optional key selects one of several lists.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Array of todo item texts, for a plan with no ordering between steps"},
					"steps": map[string]interface{}{
						"type":        "array",
						"description": "Steps with dependencies, instead of items. A step can be checked only after every step named in its after is done; a circular or unknown after is refused.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id":    map[string]interface{}{"type": "string", "description": "Short unique name other steps can refer to"},
								"text":  map[string]interface{}{"type": "string", "description": "What the step is"},
								"after": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "ids of steps that must be done first"},
							},
							"required": []string{"text"},
						},
					},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				key := scratchpadKey(ctx, args)
				if _, hasSteps := args["steps"]; hasSteps {
					if _, hasItems := args["items"]; hasItems {
						return toolErr("pass items or steps, not both"), nil
					}
					steps, err := toolArgPlanSteps(args)
					if err != nil {
						return toolErr(err.Error()), nil
					}
					list, err := pad.setSteps(key, steps)
					if err != nil {
						return scratchpadToolError(err), nil
					}
					return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
				}
				items, ok := toolArgStringSlice(args, "items")
				if !ok {
					return toolErr("items must be an array of strings (or pass steps)"), nil
				}
				list := pad.set(key, items)
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_add ---
	if !has("scratchpad_add") {
		svc.AddToolWithMetadata(
			"scratchpad_add",
			"Append one todo item to the plan list. Optional id names it; optional after lists ids of steps that must be done first. Optional key.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"text":  map[string]interface{}{"type": "string", "description": "Todo item text"},
					"id":    map[string]interface{}{"type": "string", "description": "Short unique name other steps can refer to"},
					"after": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "ids of steps that must be done before this one"},
				},
				"required": []string{"text"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				text := toolArgString(args, "text")
				if text == "" {
					return toolErr("text required"), nil
				}
				step := scratchpadItem{Text: text, ID: strings.TrimSpace(toolArgString(args, "id"))}
				if after, ok := toolArgStringSlice(args, "after"); ok {
					step.After = trimNonEmpty(after)
				}
				list, err := pad.addStep(scratchpadKey(ctx, args), step)
				if err != nil {
					return scratchpadToolError(err), nil
				}
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_check ---
	if !has("scratchpad_check") {
		svc.AddToolWithMetadata(
			"scratchpad_check",
			"Mark the todo item at position index (0-based) as done. A step that comes after unfinished steps is refused until they are done. Pass note to record what the step produced — that note is what lets this task be picked up later without redoing the work. Optional key.",
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
				list, err := pad.check(scratchpadKey(ctx, args), toolArgInt(args, "index"), toolArgString(args, "note"))
				if err != nil {
					return scratchpadToolError(err), nil
				}
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			destMeta,
		)
	}

	// --- scratchpad_note ---
	if !has("scratchpad_note") {
		svc.AddToolWithMetadata(
			"scratchpad_note",
			"Record progress on a step you have NOT finished: what you tried, what worked, what you ruled out, where you got to. Use it before a long step is interrupted — the note is all a later attempt will have to go on, and without it that attempt starts your investigation again from nothing. Use scratchpad_check instead when the step is actually done.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "List identifier, default \"default\""},
					"index": map[string]interface{}{"type": "integer", "description": "Index of the unfinished todo item (0-based)"},
					"note":  map[string]interface{}{"type": "string", "description": "Where this step has got to: the approach you settled on, the error you are chasing, what you ruled out and why."},
				},
				"required": []string{"index", "note"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				list, err := pad.note(scratchpadKey(ctx, args), toolArgInt(args, "index"), toolArgString(args, "note"))
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
				list := pad.get(scratchpadKey(ctx, args))
				return toolOK(map[string]interface{}{"items": scratchpadItemsPayload(list)}), nil
			},
			roMeta,
		)
	}
}
