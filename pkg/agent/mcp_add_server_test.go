package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets this test binary stand in for an MCP server: started with
// AGENTGO_TEST_STDIO_MCP=1 it serves one tool, "ping", over stdio and exits.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTGO_TEST_STDIO_MCP") == "1" {
		srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "probe", Version: "1"}, nil)
		sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "ping", Description: "Answers pong."},
			func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
				return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
			})
		_ = srv.Run(context.Background(), &sdkmcp.StdioTransport{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A server added while the service runs — one downloaded after start, one a
// person installs from the settings page — had its process started and its
// tools never offered: MCP tools reach the model through the search index,
// and only servers present at build, or started in the background, were put
// in it. The model searched for them and was told there were none.
func TestAServerAddedWhileRunningIsSearchable(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcpServers.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New("mcp-add").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&prefixLLM{}).
		WithMCP(WithMCPConfigPaths(cfg)).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	if err := svc.AddMCPServer(context.Background(), "probe", os.Args[0], nil, map[string]string{"AGENTGO_TEST_STDIO_MCP": "1"}); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	if !svc.toolRegistry.Has("mcp_probe_ping") {
		t.Fatalf("mcp_probe_ping is not in the registry: %v", svc.toolRegistry.Names())
	}
	if len(svc.toolRegistry.SearchAllTools("ping")) == 0 {
		t.Fatal("a search for ping finds nothing")
	}
}

// A tool the host names as eager is offered on every request instead of
// waiting behind the search index — whether it was registered before the
// rule or after. Measured: a model asked to use the browser guessed the
// tools' names, searched once, found nothing and gave up on it.
func TestEagerToolsAreOfferedWithoutASearch(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcpServers.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New("mcp-eager").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&prefixLLM{}).
		WithMCP(WithMCPConfigPaths(cfg)).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	deferred := func(name string) bool {
		svc.toolRegistry.mu.RLock()
		defer svc.toolRegistry.mu.RUnlock()
		return svc.toolRegistry.tools[name].def.DeferLoading
	}

	svc.SetEagerTools("mcp_probe_*")
	if err := svc.AddMCPServer(context.Background(), "probe", os.Args[0], nil, map[string]string{"AGENTGO_TEST_STDIO_MCP": "1"}); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	if deferred("mcp_probe_ping") {
		t.Fatal("an eager MCP tool registered after the rule is still deferred")
	}

	svc.SetEagerTools()
	svc.toolRegistry.mu.Lock()
	svc.toolRegistry.tools["mcp_probe_ping"].def.DeferLoading = true
	svc.toolRegistry.mu.Unlock()
	svc.SetEagerTools("mcp_probe_ping")
	if deferred("mcp_probe_ping") {
		t.Fatal("a tool registered before the rule stayed deferred")
	}
}
