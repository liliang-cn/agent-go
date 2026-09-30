package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A tool that declares its own output limit is cut to that, or not at all,
// whatever the service's cap says.
func TestPerToolOutputLimitOverridesTheServiceCap(t *testing.T) {
	reg := NewToolRegistry()
	noop := func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }
	def := func(name string) domain.ToolDefinition {
		return domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: name}}
	}
	reg.RegisterWithMetadata(def("fanout"), noop, CategoryCustom, ToolMetadata{OutputLimit: -1})
	reg.RegisterWithMetadata(def("tight"), noop, CategoryCustom, ToolMetadata{OutputLimit: 500})
	reg.RegisterWithMetadata(def("plain"), noop, CategoryCustom, ToolMetadata{})
	svc := &Service{toolOutputLimit: 3000, toolRegistry: reg}

	big := numberedText(4000, "x")
	result := &domain.GenerationResult{ToolCalls: []domain.ToolCall{
		{ID: "a", Type: "function", Function: domain.FunctionCall{Name: "fanout"}},
		{ID: "b", Type: "function", Function: domain.FunctionCall{Name: "tight"}},
		{ID: "c", Type: "function", Function: domain.FunctionCall{Name: "plain"}},
	}}
	msgs, cuts := svc.appendToolRoundToMessages(nil, "", result, []ToolExecutionResult{
		{ToolCallID: "a", ToolName: "fanout", Result: big},
		{ToolCallID: "b", ToolName: "tight", Result: big},
		{ToolCallID: "c", ToolName: "plain", Result: big},
	})
	if msgs[1].Content != big {
		t.Fatalf("an uncapped tool was cut to %d bytes", len(msgs[1].Content))
	}
	if len(msgs[2].Content) > 500 || !strings.Contains(msgs[2].Content, "omitted") {
		t.Fatalf("a tool with its own limit was cut to %d, want <= 500 with a marker", len(msgs[2].Content))
	}
	if len(msgs[3].Content) > 3000 || len(msgs[3].Content) <= 500 {
		t.Fatalf("a tool with no limit of its own did not get the service cap: %d", len(msgs[3].Content))
	}
	if len(cuts) != 2 || cuts[0].ToolName != "tight" || cuts[1].ToolName != "plain" {
		t.Fatalf("cuts = %+v", cuts)
	}
}
