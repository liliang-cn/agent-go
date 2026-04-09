package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

type ptcSearchTestMCP struct {
	tools []domain.ToolDefinition
}

func (m *ptcSearchTestMCP) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{"tool": toolName}, nil
}

func (m *ptcSearchTestMCP) ListTools() []domain.ToolDefinition { return m.tools }

func (m *ptcSearchTestMCP) AddServer(ctx context.Context, name string, command string, args []string) error {
	return nil
}

func TestPTCAvailableCallTools_HidesDeferredToolsUntilActivated(t *testing.T) {
	registry := NewToolRegistry()

	searchDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "search_available_tools",
			Description: "search tools",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
	deferredDef := domain.ToolDefinition{
		Type:         "function",
		DeferLoading: true,
		Function: domain.ToolFunction{
			Name:        "get_weather",
			Description: "Get weather for a location",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
	loadedDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "task_complete",
			Description: "Finish task",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}

	registry.Register(searchDef, func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryCustom)
	registry.Register(deferredDef, func(context.Context, map[string]interface{}) (interface{}, error) { return "sunny", nil }, CategoryCustom)
	registry.Register(loadedDef, func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryCustom)

	router := ptc.NewAgentGoRouter()
	registry.SyncToPTCRouter(router)

	svc := &Service{
		toolRegistry:     registry,
		currentSessionID: "session-ptc-1",
		cfg:              nil,
	}
	svc.ptcIntegration = &PTCIntegration{
		config: &PTCConfig{Enabled: true},
		router: router,
	}

	before := svc.ptcAvailableCallTools(context.Background())
	if containsToolInfoName(before, "get_weather") {
		t.Fatalf("expected deferred tool to stay hidden before activation, got %+v", before)
	}
	if !containsToolInfoName(before, "search_available_tools") {
		t.Fatalf("expected discovery tool to remain visible, got %+v", before)
	}

	registry.ActivateForSession("session-ptc-1", "get_weather")

	after := svc.ptcAvailableCallTools(context.Background())
	if !containsToolInfoName(after, "get_weather") {
		t.Fatalf("expected deferred tool to become visible after activation, got %+v", after)
	}
}

func containsToolInfoName(tools []ptc.ToolInfo, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestPTCAvailableCallTools_HidesDynamicMCPToolsUntilActivated(t *testing.T) {
	registry := NewToolRegistry()
	searchDef := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "search_available_tools",
			Description: "search tools",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
	registry.Register(searchDef, func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, CategoryCustom)

	mcpTool := domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "mcp_filesystem_read_file",
			Description: "Read a file from disk",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
	mcpSvc := &ptcSearchTestMCP{tools: []domain.ToolDefinition{mcpTool}}

	router := ptc.NewAgentGoRouter(buildPTCRouterOptions(mcpSvc, nil)...)
	registry.SyncToPTCRouter(router)

	svc := &Service{
		toolRegistry:     registry,
		mcpService:       mcpSvc,
		currentSessionID: "session-ptc-mcp-1",
	}
	svc.ptcIntegration = &PTCIntegration{
		config: &PTCConfig{Enabled: true},
		router: router,
	}

	before := svc.ptcAvailableCallTools(context.Background())
	if containsToolInfoName(before, "mcp_filesystem_read_file") {
		t.Fatalf("expected dynamic MCP tool to stay hidden before activation, got %+v", before)
	}
	if !containsToolInfoName(before, "search_available_tools") {
		t.Fatalf("expected discovery tool to remain visible, got %+v", before)
	}

	registry.ActivateForSession("session-ptc-mcp-1", "mcp_filesystem_read_file")

	after := svc.ptcAvailableCallTools(context.Background())
	if !containsToolInfoName(after, "mcp_filesystem_read_file") {
		t.Fatalf("expected dynamic MCP tool to become visible after activation, got %+v", after)
	}
}
