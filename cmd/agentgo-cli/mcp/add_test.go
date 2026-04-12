package mcp

import (
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	agentgomcp "github.com/liliang-cn/agent-go/v2/pkg/mcp"
)

func TestGetConfigFilePathUsesLoadedRuntimeHome(t *testing.T) {
	previousCfg := Cfg
	t.Cleanup(func() { Cfg = previousCfg })

	home := t.TempDir()
	Cfg = &config.Config{
		Home: home,
		MCP:  agentgomcp.DefaultConfig(),
	}

	want := filepath.Join(home, "mcpServers.json")
	if got := getConfigFilePath(); got != want {
		t.Fatalf("getConfigFilePath() = %q, want %q", got, want)
	}
}
