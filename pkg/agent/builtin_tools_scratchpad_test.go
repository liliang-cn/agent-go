package agent

import (
	"testing"
)

func TestRegisterScratchpadToolsRegistersExpectedNames(t *testing.T) {
	svc := &Service{toolRegistry: NewToolRegistry()}
	RegisterScratchpadTools(svc)
	for _, name := range []string{"scratchpad_set", "scratchpad_add", "scratchpad_check", "scratchpad_get"} {
		if !svc.toolRegistry.Has(name) {
			t.Errorf("expected %q to be registered", name)
		}
	}
}

func TestScratchpadSetAddCheckGet(t *testing.T) {
	svc := &Service{toolRegistry: NewToolRegistry()}
	RegisterScratchpadTools(svc)

	key := "TestScratchpadSetAddCheckGet"
	mustOK(t, "scratchpad_set", callTool(t, svc, "scratchpad_set", map[string]interface{}{
		"key":   key,
		"items": []interface{}{"step one", "step two"},
	}))

	data := mustOK(t, "scratchpad_add", callTool(t, svc, "scratchpad_add", map[string]interface{}{
		"key": key, "text": "step three",
	}))
	items, _ := data["items"].([]map[string]interface{})
	if len(items) != 3 {
		t.Fatalf("expected 3 items after add, got %+v", data)
	}

	data = mustOK(t, "scratchpad_check", callTool(t, svc, "scratchpad_check", map[string]interface{}{
		"key": key, "index": 1,
	}))
	items, _ = data["items"].([]map[string]interface{})
	if done, _ := items[1]["done"].(bool); !done {
		t.Fatalf("expected item 1 to be done, got %+v", items[1])
	}
	if done, _ := items[0]["done"].(bool); done {
		t.Fatalf("expected item 0 to remain undone, got %+v", items[0])
	}

	// out-of-range check fails
	res := callTool(t, svc, "scratchpad_check", map[string]interface{}{"key": key, "index": 99})
	if ok, _ := res["ok"].(bool); ok {
		t.Fatalf("expected out-of-range check to fail, got %+v", res)
	}

	data = mustOK(t, "scratchpad_get", callTool(t, svc, "scratchpad_get", map[string]interface{}{"key": key}))
	items, _ = data["items"].([]map[string]interface{})
	if len(items) != 3 {
		t.Fatalf("expected 3 items on get, got %+v", data)
	}
	if idx, _ := items[0]["index"].(int); idx != 0 {
		t.Fatalf("expected index field, got %+v", items[0])
	}
}
