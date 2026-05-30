package cortexbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// fakeToolbox implements the toolbox interface with a fixed set of defs.
type fakeToolbox struct {
	defs   []cortexdb.ToolDefinition
	called []string // names passed to Call
}

func (f *fakeToolbox) Definitions() []cortexdb.ToolDefinition { return f.defs }
func (f *fakeToolbox) Call(_ context.Context, name string, _ json.RawMessage) (any, error) {
	f.called = append(f.called, name)
	return map[string]string{"ok": name}, nil
}

// recordingSink implements the toolSink interface and records registrations.
type recordingSink struct {
	names    []string
	meta     map[string]agent.ToolMetadata
	handlers map[string]func(context.Context, map[string]interface{}) (interface{}, error)
}

func newSink() *recordingSink {
	return &recordingSink{
		meta:     map[string]agent.ToolMetadata{},
		handlers: map[string]func(context.Context, map[string]interface{}) (interface{}, error){},
	}
}

func (s *recordingSink) AddToolWithMetadata(name, _ string, _ map[string]interface{},
	handler func(context.Context, map[string]interface{}) (interface{}, error), metadata agent.ToolMetadata) {
	s.names = append(s.names, name)
	s.meta[name] = metadata
	s.handlers[name] = handler
}

func sampleDefs() []cortexdb.ToolDefinition {
	return []cortexdb.ToolDefinition{
		{Name: "knowledge_graph_query", Description: "q", InputSchema: map[string]any{"type": "object"}},
		{Name: "knowledge_graph_upsert", Description: "u", InputSchema: map[string]any{"type": "object"}},
		{Name: "knowledge_graph_delete", Description: "d", InputSchema: map[string]any{"type": "object"}},
	}
}

func TestRegisterAll(t *testing.T) {
	tb := &fakeToolbox{defs: sampleDefs()}
	sink := newSink()

	got, err := register(sink, tb)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 tools registered, got %v", got)
	}

	// read-only classification: query is read-only, upsert/delete are not.
	if !sink.meta["knowledge_graph_query"].ReadOnly {
		t.Errorf("knowledge_graph_query should be ReadOnly")
	}
	if sink.meta["knowledge_graph_upsert"].ReadOnly {
		t.Errorf("knowledge_graph_upsert should not be ReadOnly")
	}
	if !sink.meta["knowledge_graph_delete"].Destructive {
		t.Errorf("knowledge_graph_delete should be Destructive")
	}
}

func TestRegisterAllowList(t *testing.T) {
	tb := &fakeToolbox{defs: sampleDefs()}
	sink := newSink()

	got, err := register(sink, tb, WithAllow("knowledge_graph_query"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(got) != 1 || got[0] != "knowledge_graph_query" {
		t.Fatalf("allow-list mismatch: %v", got)
	}
}

func TestRegisterDenyList(t *testing.T) {
	tb := &fakeToolbox{defs: sampleDefs()}
	sink := newSink()

	got, _ := register(sink, tb, WithDeny("knowledge_graph_delete"))
	for _, n := range got {
		if n == "knowledge_graph_delete" {
			t.Fatalf("deny-list failed, delete still registered: %v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 after deny, got %v", got)
	}
}

func TestRegisterPrefixAndDispatch(t *testing.T) {
	tb := &fakeToolbox{defs: sampleDefs()}
	sink := newSink()

	got, _ := register(sink, tb, WithNamePrefix("kg_"), WithAllow("knowledge_graph_query"))
	if len(got) != 1 || got[0] != "kg_knowledge_graph_query" {
		t.Fatalf("prefix mismatch: %v", got)
	}

	// The handler must dispatch under the ORIGINAL (unprefixed) name.
	handler := sink.handlers["kg_knowledge_graph_query"]
	if handler == nil {
		t.Fatal("handler not registered under prefixed name")
	}
	if _, err := handler(context.Background(), map[string]interface{}{"query": "x"}); err != nil {
		t.Fatalf("handler call: %v", err)
	}
	if len(tb.called) != 1 || tb.called[0] != "knowledge_graph_query" {
		t.Fatalf("expected dispatch to original name, got %v", tb.called)
	}
}
