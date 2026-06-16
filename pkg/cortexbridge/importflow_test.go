package cortexbridge

import (
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// TestRegisterToolboxImportFlowMetadata verifies the importflow tool names are
// classified correctly (plan read-only, run destructive) through the shared
// registrar, using the same fake-sink/fake-toolbox pattern as the GraphRAG tests.
func TestRegisterToolboxImportFlowMetadata(t *testing.T) {
	tb := &fakeToolbox{defs: []cortexdb.ToolDefinition{
		{Name: "importflow_plan", Description: "plan", InputSchema: map[string]any{"type": "object"}},
		{Name: "importflow_run", Description: "run", InputSchema: map[string]any{"type": "object"}},
	}}
	sink := newSink()
	got, err := register(sink, tb)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tools, got %v", got)
	}
	if m := sink.meta["importflow_plan"]; !m.ReadOnly || m.Destructive {
		t.Errorf("importflow_plan should be read-only, got %+v", m)
	}
	if m := sink.meta["importflow_run"]; m.ReadOnly || !m.Destructive {
		t.Errorf("importflow_run should be destructive, got %+v", m)
	}
}

func TestConnectorToolMetadata(t *testing.T) {
	tb := &fakeToolbox{defs: []cortexdb.ToolDefinition{
		{Name: "connector_introspect", Description: "i", InputSchema: map[string]any{}},
		{Name: "connector_run", Description: "r", InputSchema: map[string]any{}},
		{Name: "connector_unmask", Description: "u", InputSchema: map[string]any{}},
	}}
	sink := newSink()
	if _, err := register(sink, tb); err != nil {
		t.Fatalf("register: %v", err)
	}
	if m := sink.meta["connector_introspect"]; !m.ReadOnly {
		t.Errorf("connector_introspect should be read-only, got %+v", m)
	}
	if m := sink.meta["connector_run"]; !m.Destructive {
		t.Errorf("connector_run should be destructive, got %+v", m)
	}
	if m := sink.meta["connector_unmask"]; !m.ReadOnly {
		t.Errorf("connector_unmask should be read-only, got %+v", m)
	}
}

func TestStripJSONFence(t *testing.T) {
	cases := map[string]string{
		"{\"a\":1}":                     "{\"a\":1}",
		"```json\n{\"a\":1}\n```":       "{\"a\":1}",
		"```\n{\"a\":1}\n```":           "{\"a\":1}",
		"  {\"a\":1}  ":                 "{\"a\":1}",
		"```json\n{\"x\":[1,2]}\n```\n": "{\"x\":[1,2]}",
	}
	for in, want := range cases {
		if got := stripJSONFence(in); got != want {
			t.Errorf("stripJSONFence(%q) = %q, want %q", in, got, want)
		}
	}
}
