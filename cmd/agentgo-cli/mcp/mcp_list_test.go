package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	agentgomcp "github.com/liliang-cn/agent-go/v2/pkg/mcp"
)

func TestConfiguredMCPServerRowsFormatsConfiguredServers(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcpServers.json")
	if err := os.WriteFile(configPath, []byte(`{
  "mcpServers": {
    "husky-pet": {
      "command": "/Users/liliang/.local/bin/mcp-pet-server",
      "args": []
    }
  }
}`), 0644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg := &config.Config{}
	cfg.Home = tmpDir
	cfg.MCP = agentgomcp.DefaultConfig()
	cfg.MCP.Servers = []string{configPath}

	rows, err := configuredMCPServerRows(cfg, "")
	if err != nil {
		t.Fatalf("configuredMCPServerRows failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	row := rows[0]
	if row.Name != "husky-pet" {
		t.Fatalf("unexpected name: %+v", row)
	}
	if row.Command != "/Users/liliang/.local/bin/mcp-pet-server" {
		t.Fatalf("unexpected command: %+v", row)
	}
	if row.Args != "-" || row.Env != "-" || row.Cwd != "-" {
		t.Fatalf("expected empty fields to render as '-', got %+v", row)
	}
	if row.Status != "enabled" || row.Auth != "Unsupported" {
		t.Fatalf("unexpected status/auth: %+v", row)
	}
}

func TestConfiguredMCPServerRowsFiltersBuiltInsAndByName(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcpServers.json")
	if err := os.WriteFile(configPath, []byte(`{
  "mcpServers": {
    "zeta": {"command": "/bin/zeta", "args": ["--x"]},
    "alpha": {"command": "/bin/alpha", "args": []}
  }
}`), 0644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg := &config.Config{}
	cfg.Home = tmpDir
	cfg.MCP = agentgomcp.DefaultConfig()
	cfg.MCP.Servers = []string{configPath}
	cfg.MCP.LoadedServers = []agentgomcp.ServerConfig{
		{Name: "filesystem", Type: agentgomcp.ServerTypeInProcess},
	}

	rows, err := configuredMCPServerRows(cfg, "alpha")
	if err != nil {
		t.Fatalf("configuredMCPServerRows failed: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("expected filtered alpha row, got %+v", rows)
	}
}
