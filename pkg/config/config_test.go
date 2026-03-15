package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/pkg/mcp"
	"github.com/liliang-cn/agent-go/pkg/pool"
)

func validConfig(home string) *Config {
	cfg := &Config{
		Home: home,
		Server: ServerConfig{
			Port: 7127,
			Host: "127.0.0.1",
		},
		RAG: RAGConfig{
			Enabled:        true,
			EmbeddingModel: "text-embedding-3-small",
		},
		MCP: defaultMCPConfig(),
		Skills: SkillsConfig{
			Paths: []string{"custom-skills"},
		},
		Memory: MemoryConfig{
			StoreType: "file",
		},
		Cache: CacheConfig{
			StoreType: "memory",
		},
	}
	cfg.ApplyHomeLayout()
	return cfg
}

func defaultMCPConfig() mcp.Config {
	return mcp.DefaultConfig()
}

func TestConfigValidate(t *testing.T) {
	cfg := validConfig(t.TempDir())
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestConfigValidateFailures(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"bad port", func(c *Config) { c.Server.Port = 0 }, "invalid server port"},
		{"empty host", func(c *Config) { c.Server.Host = "" }, "server host cannot be empty"},
		{"rag missing model", func(c *Config) { c.RAG.Enabled = true; c.RAG.EmbeddingModel = "" }, "embedding_model is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig(t.TempDir())
			tt.mut(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q in %v", tt.want, err)
			}
		})
	}
}

func TestApplyHomeLayout(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Home: home}
	cfg.ApplyHomeLayout()

	if got := cfg.AgentDBPath(); got != filepath.Join(home, "data", "agentgo.db") {
		t.Fatalf("unexpected agent db path: %s", got)
	}
	if got := cfg.CortexDBPath(); got != filepath.Join(home, "data", "cortex.db") {
		t.Fatalf("unexpected cortex db path: %s", got)
	}
	if got := cfg.Memory.MemoryPath; got != filepath.Join(home, "data", "memories") {
		t.Fatalf("unexpected memory path: %s", got)
	}
	
	// Test override
	cfg.Memory.MemoryPath = "/tmp/custom-memories"
	cfg.ApplyHomeLayout()
	if cfg.Memory.MemoryPath != "/tmp/custom-memories" {
		t.Fatalf("expected override to be preserved, got %s", cfg.Memory.MemoryPath)
	}
}

func TestResolveMCPServerPaths(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Home: home}
	unified := filepath.Join(home, "mcpServers.json")
	cfg.MCP.Servers = []string{unified}

	cfg.resolveMCPServerPaths()

	found := false
	for _, path := range cfg.MCP.Servers {
		if path == unified {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected unified path to be present")
	}
}

func TestLoadIsSafeForConcurrentCalls(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agentgo.toml")
	_ = os.WriteFile(configPath, []byte(`
home = "`+tmpDir+`"
[rag]
enabled = true
embedding_model = "test"
`), 0o644)

	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	_ = os.Chdir(tmpDir)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Load("")
		}()
	}
	wg.Wait()
}

func TestUnmarshalProvidersAliases(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{
			"name":            "primary",
			"base_url":        "http://localhost:11434/v1",
			"key":             "test",
			"model_name":      "gpt-test",
			"max_concurrency": 7,
		},
	}

	var providers []pool.Provider
	unmarshalProviders(raw, &providers)
	if len(providers) != 1 || providers[0].MaxConcurrency != 7 {
		t.Fatalf("unmarshal failed or alias not mapped")
	}
}

func TestEnvFallbackHelpers(t *testing.T) {
	t.Setenv("AGENTGO_TEST_STR", "val")
	if got := GetEnvOrDefault("AGENTGO_TEST_STR", "def"); got != "val" {
		t.Fatalf("env fallback failed")
	}
}
