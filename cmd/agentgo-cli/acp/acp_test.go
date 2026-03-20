package acp

import (
	"os"
	"path/filepath"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/liliang-cn/agent-go/v2/pkg/acpserver"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func TestConvertACPMCPServers(t *testing.T) {
	t.Parallel()

	servers, err := convertACPMCPServers([]acpsdk.McpServer{
		{
			Stdio: &acpsdk.McpServerStdio{
				Name:    "stdio-test",
				Command: "node",
				Args:    []string{"server.js"},
				Env: []acpsdk.EnvVariable{
					{Name: "DEBUG", Value: "1"},
				},
			},
		},
		{
			Http: &acpsdk.McpServerHttp{
				Name: "http-test",
				Url:  "http://127.0.0.1:8080/mcp",
				Headers: []acpsdk.HttpHeader{
					{Name: "Authorization", Value: "Bearer token"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("convert ACP MCP servers: %v", err)
	}

	if got := servers["stdio-test"].Command; got != "node" {
		t.Fatalf("unexpected stdio command: %q", got)
	}
	if got := servers["stdio-test"].Env["DEBUG"]; got != "1" {
		t.Fatalf("unexpected stdio env: %q", got)
	}
	if got := servers["http-test"].URL; got != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("unexpected http URL: %q", got)
	}
	if got := servers["http-test"].Headers["Authorization"]; got != "Bearer token" {
		t.Fatalf("unexpected http header: %q", got)
	}
}

func TestPrepareSessionConfigWritesTempMCPConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	base := &config.Config{Home: home}
	base.MCP.Enabled = true
	base.MCP.Servers = []string{"/existing/mcpServers.json"}

	cfg, cleanup, err := prepareSessionConfig(base, acpserver.SessionConfig{
		SessionID: "sess-1",
		MCPServers: []acpsdk.McpServer{
			{
				Stdio: &acpsdk.McpServerStdio{
					Name:    "stdio-test",
					Command: "node",
					Args:    []string{"server.js"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare session config: %v", err)
	}
	defer cleanup()

	if len(cfg.MCP.Servers) != 2 {
		t.Fatalf("expected existing + temp MCP config paths, got %v", cfg.MCP.Servers)
	}
	tmpPath := cfg.MCP.Servers[1]
	if filepath.Dir(tmpPath) != filepath.Join(home, "data", "acp") {
		t.Fatalf("unexpected temp config dir: %s", tmpPath)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("expected temp MCP config file: %v", err)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup temp MCP config: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected temp MCP config to be removed, err=%v", err)
	}
}

func TestConvertACPMCPServersRejectsSSE(t *testing.T) {
	t.Parallel()

	servers, err := convertACPMCPServers([]acpsdk.McpServer{
		{
			Sse: &acpsdk.McpServerSse{
				Name: "sse-test",
				Url:  "http://127.0.0.1:8080/sse",
				Headers: []acpsdk.HttpHeader{
					{Name: "Authorization", Value: "Bearer sse"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected SSE MCP server conversion to succeed, got %v", err)
	}
	if got := servers["sse-test"].Type; got != "sse" {
		t.Fatalf("unexpected SSE type: %q", got)
	}
	if got := servers["sse-test"].Headers["Authorization"]; got != "Bearer sse" {
		t.Fatalf("unexpected SSE header: %q", got)
	}
}
