package agent

import (
	"strings"
	"testing"

	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
)

func TestRenderStreamEventSkipsTaskCompleteToolResult(t *testing.T) {
	var out strings.Builder
	state := &streamRenderState{}

	renderStreamEvent(&out, &agentpkg.Event{
		Type:       agentpkg.EventTypeToolResult,
		ToolName:   "task_complete",
		ToolResult: "Hi! How can I help?",
	}, state)
	renderStreamEvent(&out, &agentpkg.Event{
		Type:    agentpkg.EventTypeComplete,
		Content: "Hi! How can I help?",
	}, state)

	got := out.String()
	if strings.Contains(got, "✅ Tool Success: task_complete") {
		t.Fatalf("unexpected task_complete tool success output: %q", got)
	}
	if strings.Contains(got, "📝 Result: Hi! How can I help?") {
		t.Fatalf("unexpected task_complete result output: %q", got)
	}
	if !strings.Contains(got, "Hi! How can I help?") {
		t.Fatalf("expected final content in complete output: %q", got)
	}
}

func TestRenderStreamEventKeepsNormalToolResults(t *testing.T) {
	var out strings.Builder
	state := &streamRenderState{}

	renderStreamEvent(&out, &agentpkg.Event{
		Type:       agentpkg.EventTypeToolResult,
		ToolName:   "mcp_websearch_websearch_basic",
		ToolResult: "gold price result",
	}, state)

	got := out.String()
	if !strings.Contains(got, "✅ Tool Success: mcp_websearch_websearch_basic") {
		t.Fatalf("expected normal tool result output: %q", got)
	}
	if !strings.Contains(got, "📝 Result: gold price result") {
		t.Fatalf("expected normal tool result body: %q", got)
	}
}
