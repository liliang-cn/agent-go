package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/resource"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
		{"rag missing model", func(c *Config) { c.RAG.Enabled = true; c.RAG.EmbeddingModel = "" }, "embedding_model or rag.embedding.providers is required"},
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

func TestMemoryStoreTypeHelpers(t *testing.T) {
	cfg := validConfig(t.TempDir())

	if got := cfg.GetMemoryStoreType(); got != MemoryStoreTypeFile {
		t.Fatalf("expected default file memory store type, got %s", got)
	}
	if err := cfg.SetMemoryStoreTypeString("cortex"); err != nil {
		t.Fatalf("set cortex memory store type failed: %v", err)
	}
	if got := cfg.GetMemoryStoreType(); got != MemoryStoreTypeCortex {
		t.Fatalf("expected cortex memory store type, got %s", got)
	}
	if got := cfg.MemoryPrimaryPath(); got != filepath.Join(cfg.Home, "data", "cortex.db") {
		t.Fatalf("expected cortex memory path to use cortex db, got %s", got)
	}
	if err := cfg.SetMemoryStoreTypeString("memoryflow"); err != nil {
		t.Fatalf("set memoryflow memory store type failed: %v", err)
	}
	if got := cfg.GetMemoryStoreType(); got != MemoryStoreTypeMemoryFlow {
		t.Fatalf("expected memoryflow memory store type, got %s", got)
	}
	if got := cfg.MemoryPrimaryPath(); got != filepath.Join(cfg.Home, "data", "cortex.db") {
		t.Fatalf("expected memoryflow memory path to use cortex db, got %s", got)
	}
	if err := cfg.SetMemoryStoreTypeString("graphflow"); err != nil {
		t.Fatalf("set graphflow memory store type failed: %v", err)
	}
	if got := cfg.GetMemoryStoreType(); got != MemoryStoreTypeGraphFlow {
		t.Fatalf("expected graphflow memory store type, got %s", got)
	}
	if got := cfg.MemoryPrimaryPath(); got != filepath.Join(cfg.Home, "data", "cortex.db") {
		t.Fatalf("expected graphflow memory path to use cortex db, got %s", got)
	}
	if err := cfg.SetMemoryStoreType(MemoryStoreTypeFile); err != nil {
		t.Fatalf("set file memory store type failed: %v", err)
	}
	if got := cfg.MemoryPrimaryPath(); got != filepath.Join(cfg.Home, "data", "memories") {
		t.Fatalf("expected file memory path after switching back, got %s", got)
	}
	if err := cfg.SetMemoryStoreTypeString("vector"); err == nil {
		t.Fatal("expected vector memory store type to fail")
	}
	if err := cfg.SetMemoryStoreTypeString("rag"); err == nil {
		t.Fatal("expected rag memory store type to fail")
	}
	if err := cfg.SetMemoryStoreTypeString("cortexdb"); err == nil {
		t.Fatal("expected cortexdb memory store type to fail")
	}
	if err := cfg.SetMemoryStoreTypeString("hybrid"); err == nil {
		t.Fatal("expected hybrid memory store type to fail")
	}
	if err := cfg.SetMemoryStoreTypeString("invalid"); err == nil {
		t.Fatal("expected invalid memory store type to fail")
	}
}

func TestLoadDBBackedRuntimePreservesInitialMemoryStoreType(t *testing.T) {
	cfg := validConfig(t.TempDir())
	cfg.Memory.StoreType = MemoryStoreTypeGraphFlow

	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	if err := cfg.LoadDBBackedRuntimeFrom(db); err != nil {
		t.Fatalf("load db runtime failed: %v", err)
	}
	if got := cfg.GetMemoryStoreType(); got != MemoryStoreTypeGraphFlow {
		t.Fatalf("expected graphflow memory store type, got %s", got)
	}

	value, err := db.GetConfig("memory.store_type")
	if err != nil {
		t.Fatalf("get memory.store_type failed: %v", err)
	}
	if value != string(MemoryStoreTypeGraphFlow) {
		t.Fatalf("db memory.store_type = %q, want %q", value, MemoryStoreTypeGraphFlow)
	}
}

func TestLoadDBBackedRuntimeAppliesResources(t *testing.T) {
	cfg := validConfig(t.TempDir())
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	if err := db.SaveResource(resource.Resource{ID: "memory:memoryflow", Kind: resource.KindMemory, Name: "memoryflow"}); err != nil {
		t.Fatalf("save memory resource failed: %v", err)
	}
	if err := db.SaveResource(resource.Resource{
		ID:       "rag:default",
		Kind:     resource.KindRAG,
		Name:     "default",
		Metadata: map[string]any{"embedding_model": "resource-embedding"},
	}); err != nil {
		t.Fatalf("save rag resource failed: %v", err)
	}
	if err := db.SaveResource(resource.Resource{
		ID:       "mcp:test",
		Kind:     resource.KindMCP,
		Name:     "test",
		Provider: "/tmp/mcpServers.json",
		Metadata: map[string]any{
			"server_config": map[string]any{
				"name":        "test",
				"type":        "stdio",
				"command":     []string{"uvx"},
				"args":        []string{"mcp-test"},
				"auto_start":  true,
				"working_dir": "/tmp/project",
				"env":         map[string]string{"DEBUG": "1"},
			},
		},
	}); err != nil {
		t.Fatalf("save mcp resource failed: %v", err)
	}
	if err := db.SaveResource(resource.Resource{
		ID:       "skill:test",
		Kind:     resource.KindSkill,
		Name:     "test",
		Provider: "/tmp/skills",
	}); err != nil {
		t.Fatalf("save skill resource failed: %v", err)
	}
	if err := db.SaveResource(resource.Resource{
		ID:       "llm:resource-model",
		Kind:     resource.KindLLM,
		Name:     "resource-model",
		Provider: "http://resource.example/v1",
		Metadata: map[string]any{
			"provider_name":   "resource-provider",
			"key":             "resource-key",
			"model_name":      "resource-model",
			"max_concurrency": 7,
			"capability":      5,
		},
	}); err != nil {
		t.Fatalf("save llm resource failed: %v", err)
	}
	if err := db.SaveConfig("mcp.paths", `["/tmp/legacy-mcpServers.json"]`); err != nil {
		t.Fatalf("save mcp.paths failed: %v", err)
	}
	if err := db.SaveConfig("skills.paths", `["/tmp/legacy-skills"]`); err != nil {
		t.Fatalf("save skills.paths failed: %v", err)
	}

	if err := cfg.LoadDBBackedRuntimeFrom(db); err != nil {
		t.Fatalf("load db runtime failed: %v", err)
	}
	if got := cfg.GetMemoryStoreType(); got != MemoryStoreTypeMemoryFlow {
		t.Fatalf("expected memoryflow resource to set memory store type, got %s", got)
	}
	if cfg.RAG.EmbeddingModel != "resource-embedding" || !cfg.RAG.Enabled {
		t.Fatalf("expected rag resource to set embedding model and enable rag, got %+v", cfg.RAG)
	}
	if !containsString(cfg.MCP.Servers, "/tmp/mcpServers.json") {
		t.Fatalf("expected mcp resource path in config, got %+v", cfg.MCP.Servers)
	}
	if !containsString(cfg.MCP.Servers, "/tmp/legacy-mcpServers.json") {
		t.Fatalf("expected legacy mcp path merged with resource path, got %+v", cfg.MCP.Servers)
	}
	if len(cfg.MCP.InlineServers) != 1 {
		t.Fatalf("expected one inline mcp server from resource, got %+v", cfg.MCP.InlineServers)
	}
	inlineServer := cfg.MCP.InlineServers[0]
	if inlineServer.Name != "test" ||
		inlineServer.Type != mcp.ServerTypeStdio ||
		len(inlineServer.Command) != 1 ||
		inlineServer.Command[0] != "uvx" ||
		inlineServer.WorkingDir != "/tmp/project" ||
		inlineServer.Env["DEBUG"] != "1" {
		t.Fatalf("expected inline mcp resource server to round trip, got %+v", inlineServer)
	}
	if !containsString(cfg.Skills.Paths, "/tmp/skills") {
		t.Fatalf("expected skill resource path in config, got %+v", cfg.Skills.Paths)
	}
	if !containsString(cfg.Skills.Paths, "/tmp/legacy-skills") {
		t.Fatalf("expected legacy skill path merged with resource path, got %+v", cfg.Skills.Paths)
	}
	for _, provider := range cfg.LLM.Providers {
		if provider.Name == "resource-provider" &&
			provider.BaseURL == "http://resource.example/v1" &&
			provider.ModelName == "resource-model" &&
			provider.MaxConcurrency == 7 &&
			provider.Capability == 5 {
			return
		}
	}
	t.Fatalf("expected llm resource provider in config, got %+v", cfg.LLM.Providers)
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
	t.Setenv("AGENTGO_HOME", tmpDir)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Load()
		}()
	}
	wg.Wait()
}

func TestLoadUsesAgentGoHome(t *testing.T) {
	tmpDir := t.TempDir()
	customHome := filepath.Join(tmpDir, "custom-home")
	t.Setenv("AGENTGO_HOME", customHome)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if cfg.Home != customHome {
		t.Fatalf("expected home %s, got %s", customHome, cfg.Home)
	}
}

func TestLoadUsesAgentGoDBAsRuntimeSourceOfTruth(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	t.Setenv("AGENTGO_HOME", home)

	cfg := &Config{Home: home}
	cfg.ApplyHomeLayout()
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("new agentgo db failed: %v", err)
	}
	defer db.Close()
	if err := db.SaveProvider(&store.LLMProvider{
		Name:           "from-db",
		BaseURL:        "http://db.example/v1",
		Key:            "db-key",
		ModelName:      "db-model",
		MaxConcurrency: 3,
		Capability:     4,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("save provider failed: %v", err)
	}
	if err := db.SaveConfig("llm.strategy", "least_load"); err != nil {
		t.Fatalf("save llm.strategy failed: %v", err)
	}
	if err := db.SaveConfig("rag.embedding_model", "db-embedding"); err != nil {
		t.Fatalf("save rag.embedding_model failed: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if got := string(loaded.LLM.Strategy); got != "least_load" {
		t.Fatalf("expected db-backed strategy least_load, got %q", got)
	}
	if len(loaded.LLM.Providers) != 1 || loaded.LLM.Providers[0].Name != "from-db" {
		t.Fatalf("expected db-backed provider, got %+v", loaded.LLM.Providers)
	}
	if loaded.RAG.EmbeddingModel != "db-embedding" {
		t.Fatalf("expected db-backed embedding model, got %q", loaded.RAG.EmbeddingModel)
	}
	expectedMCPPath := filepath.Join(home, "mcpServers.json")
	if len(loaded.MCP.Servers) == 0 || loaded.MCP.Servers[0] != expectedMCPPath {
		t.Fatalf("expected default MCP path %q, got %v", expectedMCPPath, loaded.MCP.Servers)
	}
}

func TestMCPServersPathsDeduplicatesDefaultHomePath(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Home: home}

	path := filepath.Join(home, "mcpServers.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("write mcpServers.json failed: %v", err)
	}

	paths := cfg.MCPServersPaths()
	count := 0
	for _, candidate := range paths {
		if candidate == path {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected %q exactly once, got %v", path, paths)
	}
}

func TestLoadBackfillsEmbeddingModelFromEnabledEmbeddingProvider(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	t.Setenv("AGENTGO_HOME", home)

	cfg := &Config{Home: home}
	cfg.ApplyHomeLayout()
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("new agentgo db failed: %v", err)
	}
	defer db.Close()

	if err := db.SaveEmbeddingProvider(&store.EmbeddingProvider{
		Name:           "embedder",
		BaseURL:        "http://embed.example/v1",
		Key:            "embed-key",
		ModelName:      "text-embedding-3-small",
		MaxConcurrency: 4,
		Capability:     4,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("save embedding provider failed: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if loaded.RAG.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("expected backfilled embedding model, got %q", loaded.RAG.EmbeddingModel)
	}

	savedValue, err := db.GetConfig("rag.embedding_model")
	if err != nil {
		t.Fatalf("get rag.embedding_model failed: %v", err)
	}
	if savedValue != "text-embedding-3-small" {
		t.Fatalf("expected persisted rag.embedding_model, got %q", savedValue)
	}
}

func TestEnvFallbackHelpers(t *testing.T) {
	t.Setenv("AGENTGO_TEST_STR", "val")
	if got := GetEnvOrDefault("AGENTGO_TEST_STR", "def"); got != "val" {
		t.Fatalf("env fallback failed")
	}
}

func TestDefaultConfigUsesEnvForAgentLLMTurnTimeout(t *testing.T) {
	t.Setenv("AGENTGO_LLM_TURN_TIMEOUT_SECONDS", "240")
	cfg := defaultConfig(t.TempDir())
	if got := cfg.AgentLLMTurnTimeout(); got != 240*time.Second {
		t.Fatalf("expected 240s agent llm turn timeout, got %v", got)
	}
}

func TestAgentLLMTurnTimeoutFallsBackToDefaultWhenUnset(t *testing.T) {
	cfg := &Config{}
	if got := cfg.AgentLLMTurnTimeout(); got != 180*time.Second {
		t.Fatalf("expected default 180s timeout, got %v", got)
	}
}
