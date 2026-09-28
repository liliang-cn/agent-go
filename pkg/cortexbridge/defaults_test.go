package cortexbridge

import (
	"context"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// hostSink is a sink with memory and tools of its own, like *agent.Service.
type hostSink struct {
	*recordingSink
	memory bool
	tools  map[string]bool
	user   string
}

func (h *hostSink) HasMemory() bool                     { return h.memory }
func (h *hostSink) MemoryUserID(context.Context) string { return h.user }
func (h *hostSink) HasTool(name string) bool            { return h.tools[name] }

func memoryDefs() []cortexdb.ToolDefinition {
	defs := []cortexdb.ToolDefinition{{Name: "knowledge_search", InputSchema: map[string]any{"type": "object"}}}
	for _, n := range MemoryTools {
		defs = append(defs, cortexdb.ToolDefinition{Name: n, InputSchema: map[string]any{"type": "object"}})
	}
	return defs
}

// A service with memory of its own gets one memory path by default; without
// memory, CortexDB's tools are the only memory and stay.
func TestMemoryToolsLeftOutBesideAgentMemory(t *testing.T) {
	with := &hostSink{recordingSink: newSink(), memory: true}
	names, err := register(with, &fakeToolbox{defs: memoryDefs()})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "knowledge_search" {
		t.Fatalf("with agent memory registered %v, want only knowledge_search", names)
	}

	without := &hostSink{recordingSink: newSink()}
	names, _ = register(without, &fakeToolbox{defs: memoryDefs()})
	if len(names) != 1+len(MemoryTools) {
		t.Fatalf("without agent memory registered %v, want every tool", names)
	}

	optIn := &hostSink{recordingSink: newSink(), memory: true}
	names, _ = register(optIn, &fakeToolbox{defs: memoryDefs()}, WithCortexMemoryTools())
	if len(names) != 1+len(MemoryTools) {
		t.Fatalf("WithCortexMemoryTools registered %v, want every tool", names)
	}
}

// A CortexDB tool never replaces one the host already has; a prefix keeps both.
func TestHostToolsAreNeverReplaced(t *testing.T) {
	h := &hostSink{recordingSink: newSink(), memory: true, tools: map[string]bool{"memory_save": true}}
	names, _ := register(h, &fakeToolbox{defs: memoryDefs()}, WithCortexMemoryTools())
	for _, n := range names {
		if n == "memory_save" {
			t.Fatal("memory_save was re-registered over the host's own")
		}
	}
	h = &hostSink{recordingSink: newSink(), memory: true, tools: map[string]bool{"memory_save": true}}
	names, _ = register(h, &fakeToolbox{defs: memoryDefs()}, WithCortexMemoryTools(), WithNamePrefix("kg_"))
	found := false
	for _, n := range names {
		found = found || n == "kg_memory_save"
	}
	if !found {
		t.Fatalf("prefixed memory_save missing from %v", names)
	}
}

// user_id comes from the run's memory scope with no option at all; an explicit
// default beats it, and the model's own value beats both.
func TestUserIDFromMemoryScope(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"user_id": map[string]any{"type": "string"}}}
	defs := []cortexdb.ToolDefinition{{Name: "knowledge_memory_remember", InputSchema: schema}}
	ctx := context.Background()

	tb := &inputToolbox{defs: defs, inputs: map[string]map[string]interface{}{}}
	h := &hostSink{recordingSink: newSink(), user: "u-1"}
	_, _ = register(h, tb)
	_, _ = h.handlers["knowledge_memory_remember"](ctx, map[string]interface{}{})
	if got := tb.inputs["knowledge_memory_remember"]["user_id"]; got != "u-1" {
		t.Fatalf("user_id = %v, want the scope's u-1", got)
	}

	tb = &inputToolbox{defs: defs, inputs: map[string]map[string]interface{}{}}
	h = &hostSink{recordingSink: newSink(), user: "u-1"}
	_, _ = register(h, tb, WithArgDefaults(map[string]interface{}{"user_id": "owner"}))
	_, _ = h.handlers["knowledge_memory_remember"](ctx, map[string]interface{}{})
	if got := tb.inputs["knowledge_memory_remember"]["user_id"]; got != "owner" {
		t.Fatalf("user_id = %v, want the explicit default", got)
	}
	_, _ = h.handlers["knowledge_memory_remember"](ctx, map[string]interface{}{"user_id": "alice"})
	if got := tb.inputs["knowledge_memory_remember"]["user_id"]; got != "alice" {
		t.Fatalf("user_id = %v, want the model's alice", got)
	}

	tb = &inputToolbox{defs: defs, inputs: map[string]map[string]interface{}{}}
	h = &hostSink{recordingSink: newSink()}
	_, _ = register(h, tb)
	_, _ = h.handlers["knowledge_memory_remember"](ctx, map[string]interface{}{})
	if _, has := tb.inputs["knowledge_memory_remember"]["user_id"]; has {
		t.Fatal("user_id was invented with no scope to take it from")
	}
}
