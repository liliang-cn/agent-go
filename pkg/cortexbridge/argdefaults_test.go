package cortexbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

type inputToolbox struct {
	defs   []cortexdb.ToolDefinition
	inputs map[string]map[string]interface{}
}

func (f *inputToolbox) Definitions() []cortexdb.ToolDefinition { return f.defs }
func (f *inputToolbox) Call(_ context.Context, name string, raw json.RawMessage) (any, error) {
	var m map[string]interface{}
	_ = json.Unmarshal(raw, &m)
	f.inputs[name] = m
	return "ok", nil
}

// user_id is filled where the schema declares it and the call left it out;
// a value the model gave is kept, and a tool without the argument never
// receives it.
func TestArgDefaultsFillOnlyDeclaredAbsentArguments(t *testing.T) {
	withUser := map[string]any{"type": "object", "properties": map[string]any{"user_id": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}
	without := map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}
	tb := &inputToolbox{inputs: map[string]map[string]interface{}{}, defs: []cortexdb.ToolDefinition{
		{Name: "knowledge_memory_remember", InputSchema: withUser},
		{Name: "knowledge_search", InputSchema: without},
	}}
	sink := newSink()
	if _, err := register(sink, tb, WithArgDefaults(map[string]interface{}{"user_id": "owner"})); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	_, _ = sink.handlers["knowledge_memory_remember"](ctx, map[string]interface{}{"content": "x"})
	if got := tb.inputs["knowledge_memory_remember"]["user_id"]; got != "owner" {
		t.Fatalf("absent user_id = %v, want the default", got)
	}
	_, _ = sink.handlers["knowledge_memory_remember"](ctx, map[string]interface{}{"content": "x", "user_id": "alice"})
	if got := tb.inputs["knowledge_memory_remember"]["user_id"]; got != "alice" {
		t.Fatalf("a supplied user_id was overwritten: %v", got)
	}
	_, _ = sink.handlers["knowledge_search"](ctx, map[string]interface{}{"query": "q"})
	if _, has := tb.inputs["knowledge_search"]["user_id"]; has {
		t.Fatal("a tool that does not declare user_id received it")
	}
}

// MemoryTools covers CortexDB's own memory tools, so a host with agent-go
// memory can keep one memory path.
func TestMemoryToolsCanBeDenied(t *testing.T) {
	var defs []cortexdb.ToolDefinition
	for _, n := range append([]string{"knowledge_search"}, MemoryTools...) {
		defs = append(defs, cortexdb.ToolDefinition{Name: n, InputSchema: map[string]any{"type": "object"}})
	}
	sink := newSink()
	names, err := register(sink, &inputToolbox{defs: defs, inputs: map[string]map[string]interface{}{}}, WithDeny(MemoryTools...))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "knowledge_search" {
		t.Fatalf("registered %v, want only knowledge_search", names)
	}
}
