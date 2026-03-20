package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type webSearchTestMCP struct {
	tools []domain.ToolDefinition
}

func (m *webSearchTestMCP) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func (m *webSearchTestMCP) ListTools() []domain.ToolDefinition {
	return m.tools
}

func (m *webSearchTestMCP) AddServer(ctx context.Context, name string, command string, args []string) error {
	return nil
}

func TestCollectAllAvailableToolsHidesMCPWebSearchWhenNativeLikeModes(t *testing.T) {
	baseTools := []domain.ToolDefinition{
		{Type: "function", Function: domain.ToolFunction{Name: "mcp_websearch_lookup"}},
		{Type: "function", Function: domain.ToolFunction{Name: "mcp_filesystem_read_file"}},
	}

	tests := []struct {
		name         string
		mode         string
		wantSearch   bool
		wantFileRead bool
	}{
		{name: "native hides websearch", mode: "native", wantSearch: false, wantFileRead: true},
		{name: "off hides websearch", mode: "off", wantSearch: false, wantFileRead: true},
		{name: "mcp keeps websearch", mode: "mcp", wantSearch: true, wantFileRead: true},
		{name: "auto keeps websearch fallback", mode: "auto", wantSearch: true, wantFileRead: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{
				cfg: &config.Config{
					Tooling: config.ToolingConfig{
						WebSearch: config.WebSearchConfig{
							Mode: tt.mode,
						},
					},
				},
				mcpService:       &webSearchTestMCP{tools: baseTools},
				toolRegistry:     NewToolRegistry(),
				currentSessionID: "session-1",
			}

			tools := svc.collectAllAvailableTools(context.Background(), nil)
			names := make(map[string]bool, len(tools))
			for _, tool := range tools {
				names[tool.Function.Name] = true
			}

			if names["mcp_websearch_lookup"] != tt.wantSearch {
				t.Fatalf("mode=%s websearch presence=%v want %v", tt.mode, names["mcp_websearch_lookup"], tt.wantSearch)
			}
			if names["mcp_filesystem_read_file"] != tt.wantFileRead {
				t.Fatalf("mode=%s filesystem presence=%v want %v", tt.mode, names["mcp_filesystem_read_file"], tt.wantFileRead)
			}
		})
	}
}

func TestToolGenerationOptionsUsesConfiguredWebSearchMode(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{
					Mode:              "auto",
					SearchContextSize: "high",
				},
			},
		},
	}

	opts := svc.toolGenerationOptions(0.3, 1200, "auto")
	if opts.WebSearchMode != domain.WebSearchModeAuto {
		t.Fatalf("expected auto web search mode, got %q", opts.WebSearchMode)
	}
	if opts.WebSearchContextSize != "high" {
		t.Fatalf("expected high context size, got %q", opts.WebSearchContextSize)
	}
	if opts.ToolChoice != "auto" {
		t.Fatalf("expected tool choice auto, got %q", opts.ToolChoice)
	}
}

func TestSearchToolRedirectOnlyInterceptsWebSearchToolDiscovery(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{
					Mode: "native",
				},
			},
		},
		toolRegistry: NewToolRegistry(),
		mcpService: &webSearchTestMCP{tools: []domain.ToolDefinition{
			{
				Type: "function",
				Function: domain.ToolFunction{
					Name:        "mcp_websearch_websearch_ai_summary",
					Description: "Search the web for current information and summarize the results.",
				},
			},
			{
				Type: "function",
				Function: domain.ToolFunction{
					Name:        "mcp_filesystem_read_file",
					Description: "Read a local file from disk.",
				},
			},
		}},
	}

	redirect, ok := svc.searchToolRedirect("gold price", "find a tool that can search the web for the current gold price", "mcp_websearch")
	if !ok {
		t.Fatal("expected native web search redirect")
	}
	if !strings.Contains(strings.ToLower(redirect), "native web search") {
		t.Fatalf("unexpected redirect message: %q", redirect)
	}

	redirect, ok = svc.searchToolRedirect("web search summary", "find a tool that can search the web", "")
	if !ok {
		t.Fatal("expected redirect when query matches hidden web search tool metadata")
	}

	svc.cfg.Tooling.WebSearch.Mode = "auto"
	redirect, ok = svc.searchToolRedirect("gold price", "find a tool that can search the web for the current gold price", "mcp_websearch")
	if ok {
		t.Fatalf("did not expect redirect in auto mode, got %q", redirect)
	}

	redirect, ok = svc.searchToolRedirect("gold price", "answer the user's gold price question", "")
	if ok {
		t.Fatalf("did not expect redirect for plain task text without hidden tool discovery, got %q", redirect)
	}

	redirect, ok = svc.searchToolRedirect("filesystem read tools", "find tools to read files", "mcp_filesystem")
	if ok {
		t.Fatalf("did not expect redirect for non-web tool search, got %q", redirect)
	}
}

func TestBuildWebSearchPromptNote(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{
					Mode: "native",
				},
			},
		},
	}

	note := svc.buildWebSearchPromptNote(nil)
	if !strings.Contains(strings.ToLower(note), "native web search capability") {
		t.Fatalf("expected native web search note, got %q", note)
	}
	if !strings.Contains(strings.ToLower(note), "do not search the tool catalog") {
		t.Fatalf("expected tool catalog guidance, got %q", note)
	}

	svc.cfg.Tooling.WebSearch.Mode = "auto"
	note = svc.buildWebSearchPromptNote(nil)
	if !strings.Contains(strings.ToLower(note), "fallback") {
		t.Fatalf("expected auto-mode fallback note, got %q", note)
	}
}

func TestBuildWebSearchPromptNoteSkipsConcierge(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{
			Tooling: config.ToolingConfig{
				WebSearch: config.WebSearchConfig{
					Mode: "auto",
				},
			},
		},
	}

	note := svc.buildWebSearchPromptNote(NewAgentWithConfig(BuiltInConciergeAgentName, "concierge", nil))
	if note != "" {
		t.Fatalf("expected concierge to skip web search note, got %q", note)
	}
}
