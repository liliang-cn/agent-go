package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// stubMCP lists a fixed tool set.
type stubMCP struct{ tools []domain.ToolDefinition }

func (m *stubMCP) ListTools() []domain.ToolDefinition { return m.tools }
func (m *stubMCP) CallTool(context.Context, string, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (m *stubMCP) AddServer(context.Context, string, string, []string) error { return nil }

func mcpTool(name string) domain.ToolDefinition {
	return domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: name}}
}

// An embedder that registers the built-in web_search and also runs the MCP
// websearch server gives the model two ways to do one thing, and the model uses
// both — the audit found single questions answered with a web_search call and
// an mcp_websearch_basic call back to back. The configured mode decides.
func TestOnlyOneWebSearchRouteIsOffered(t *testing.T) {
	t.Parallel()

	newSvc := func(mode string, mcpTools []domain.ToolDefinition) *Service {
		cfg := &config.Config{}
		cfg.Tooling.WebSearch.Mode = mode
		svc := &Service{cfg: cfg, toolRegistry: NewToolRegistry()}
		svc.toolRegistry.Register(mcpTool(registryWebSearchToolName), nil, CategoryCustom)
		svc.toolRegistry.Register(mcpTool("fs_read"), nil, CategoryCustom)
		if mcpTools != nil {
			svc.mcpService = &stubMCP{tools: mcpTools}
		}
		return svc
	}
	names := func(svc *Service) []string {
		tools := svc.collectAllAvailableTools(context.Background(), NewAgent("Responder"))
		out := make([]string, 0, len(tools))
		for _, t := range tools {
			out = append(out, t.Function.Name)
		}
		return out
	}

	// mcp mode with the MCP route present: the registry tool is redundant.
	got := names(newSvc("mcp", []domain.ToolDefinition{mcpTool("mcp_websearch_basic")}))
	if containsStr(got, registryWebSearchToolName) {
		t.Errorf("both search routes offered at once: %v", got)
	}
	if !containsStr(got, "mcp_websearch_basic") {
		t.Errorf("the configured MCP route was dropped: %v", got)
	}
	if !containsStr(got, "fs_read") {
		t.Errorf("an unrelated tool went missing: %v", got)
	}

	// mcp mode with NO MCP websearch running: dropping the registry tool would
	// leave the run unable to look anything up. That is the bug we just fixed
	// elsewhere; do not reintroduce it here.
	got = names(newSvc("mcp", []domain.ToolDefinition{mcpTool("mcp_files_read")}))
	if !containsStr(got, registryWebSearchToolName) {
		t.Errorf("the only search route was removed: %v", got)
	}

	// No MCP service at all: same reasoning.
	got = names(newSvc("mcp", nil))
	if !containsStr(got, registryWebSearchToolName) {
		t.Errorf("the only search route was removed with no MCP service: %v", got)
	}

	// native mode is the mirror image: the registry tool stays, MCP goes.
	got = names(newSvc("native", []domain.ToolDefinition{mcpTool("mcp_websearch_basic")}))
	if !containsStr(got, registryWebSearchToolName) {
		t.Errorf("native mode dropped the native tool: %v", got)
	}
	if containsStr(got, "mcp_websearch_basic") {
		t.Errorf("native mode kept the MCP route: %v", got)
	}
}
