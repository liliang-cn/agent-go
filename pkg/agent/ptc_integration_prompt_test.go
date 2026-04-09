package agent

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

func TestGetPTCSystemPrompt_RemovesSearchGuidance(t *testing.T) {
	p := &PTCIntegration{
		config: &PTCConfig{Enabled: true},
	}

	prompt := p.GetPTCSystemPrompt([]ptc.ToolInfo{
		{Name: "mcp_filesystem_write_file"},
		{Name: "search_available_tools"},
	})

	if strings.Contains(prompt, "searchAndCallTool") {
		t.Fatalf("expected prompt to avoid searchAndCallTool guidance, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use `callTool(name, args)`") {
		t.Fatalf("expected prompt to keep callTool guidance, got %q", prompt)
	}
	if !strings.Contains(prompt, "first call `search_available_tools`") {
		t.Fatalf("expected prompt to recommend search_available_tools discovery, got %q", prompt)
	}
}

func TestGetPTCTools_OnlyExposeExecuteJavascript(t *testing.T) {
	p := &PTCIntegration{
		config: &PTCConfig{Enabled: true},
	}

	tools := p.GetPTCTools([]ptc.ToolInfo{
		{Name: "mcp_filesystem_write_file"},
		{Name: "mcp_websearch_websearch_basic"},
	})

	if len(tools) != 1 {
		t.Fatalf("expected only execute_javascript tool, got %d", len(tools))
	}
	if tools[0].Function.Name != "execute_javascript" {
		t.Fatalf("unexpected tool name: %s", tools[0].Function.Name)
	}
	if strings.Contains(tools[0].Function.Description, "searchAndCallTool") {
		t.Fatalf("expected execute_javascript description to avoid searchAndCallTool, got %q", tools[0].Function.Description)
	}
	if !strings.Contains(tools[0].Function.Description, "search_available_tools") {
		t.Fatalf("expected execute_javascript description to mention tool discovery, got %q", tools[0].Function.Description)
	}
}
