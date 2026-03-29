package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/pool"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

var configLoadMu sync.Mutex

// Config 是 AgentGo 的全局配置结构
type Config struct {
	Home    string        `mapstructure:"home"`
	Debug   bool          `mapstructure:"debug"`
	Server  ServerConfig  `mapstructure:"server"`
	LLM     LLMConfig     `mapstructure:"llm"`
	RAG     RAGConfig     `mapstructure:"rag"`
	MCP     mcp.Config    `mapstructure:"mcp"`
	Skills  SkillsConfig  `mapstructure:"skills"`
	Memory  MemoryConfig  `mapstructure:"memory"`
	Cache   CacheConfig   `mapstructure:"cache"`
	Tooling ToolingConfig `mapstructure:"tooling"`
	Agent   AgentConfig   `mapstructure:"agent"`
	Team    TeamConfig    `mapstructure:"team"`

	// Internal storage configs (not exposed to user directly)
	Internal struct {
		Storage CortexdbConfig `mapstructure:"-"`
	} `mapstructure:"-"`
}

// AgentConfig holds global agent settings.
type AgentConfig struct {
	Name string `mapstructure:"name"`
}

// TeamConfig holds global team settings.
type TeamConfig struct {
	Name string `mapstructure:"name"`
}

type ServerConfig struct {
	Port        int      `mapstructure:"port"`
	Host        string   `mapstructure:"host"`
	EnableUI    bool     `mapstructure:"enable_ui"`
	CORSOrigins []string `mapstructure:"cors_origins"`
}

type LLMConfig struct {
	Strategy  pool.SelectionStrategy `mapstructure:"strategy"`
	Providers []pool.Provider        `mapstructure:"providers"`
}

// EmbeddingConfig holds dedicated embedding provider settings.
type EmbeddingConfig struct {
	Strategy  pool.SelectionStrategy `mapstructure:"strategy"`
	Providers []pool.Provider        `mapstructure:"providers"`
}

// RAGConfig holds RAG settings including optional dedicated embedding providers.
type RAGConfig struct {
	Enabled        bool            `mapstructure:"enabled"`
	EmbeddingModel string          `mapstructure:"embedding_model"`
	Embedding      EmbeddingConfig `mapstructure:"embedding"`
}

// CortexdbConfig 内部存储配置
type CortexdbConfig struct {
	DBPath    string
	MaxConns  int
	BatchSize int
	TopK      int
	Threshold float64
	IndexType string
}

type SkillsConfig struct {
	Paths                 []string `mapstructure:"paths"`
	AutoLoad              bool     `mapstructure:"auto_load"`
	AllowCommandInjection bool     `mapstructure:"allow_command_injection"`
	RequireConfirmation   bool     `mapstructure:"require_confirmation"`
}

type MemoryConfig struct {
	StoreType   MemoryStoreType `mapstructure:"store_type"`
	MemoryPath  string          `mapstructure:"memory_path"`
	MaxMemories int             `mapstructure:"max_memories"`
}

type CacheConfig struct {
	StoreType string `mapstructure:"store_type"`
	Path      string `mapstructure:"path"`
	MaxSize   int    `mapstructure:"max_size"`
}

type ToolingConfig struct {
	SavingMode        bool            `mapstructure:"saving_mode"`
	EnableSearchTools bool            `mapstructure:"enable_search_tools"`
	WebSearch         WebSearchConfig `mapstructure:"web_search"`
}

type WebSearchConfig struct {
	Mode              string `mapstructure:"mode"`
	SearchContextSize string `mapstructure:"search_context_size"`
}

// --- 路径推导 (Single Source of Truth) ---

func (c *Config) DataDir() string      { return filepath.Join(c.Home, "data") }
func (c *Config) ConfigDir() string    { return filepath.Join(c.Home, "config") }
func (c *Config) SkillsDir() string    { return filepath.Join(c.Home, "skills") }
func (c *Config) IntentsDir() string   { return filepath.Join(c.Home, "intents") }
func (c *Config) WorkspaceDir() string { return filepath.Join(c.Home, "workspace") }
func (c *Config) AgentDBPath() string  { return filepath.Join(c.DataDir(), "agentgo.db") }
func (c *Config) CortexDBPath() string { return filepath.Join(c.DataDir(), "cortex.db") }

// ApplyHomeLayout 唯一的路径计算枢纽
func (c *Config) ApplyHomeLayout() {
	c.Home = expandHomePath(c.Home)

	// 1. 初始化内部存储参数
	c.Internal.Storage.DBPath = c.CortexDBPath()
	c.Internal.Storage.MaxConns = 10
	c.Internal.Storage.BatchSize = 100
	c.Internal.Storage.TopK = 5
	c.Internal.Storage.IndexType = "hnsw"

	// 2. 自动对齐 Memory 路径
	c.applyMemoryLayout()

	// 3. 自动对齐 Cache 路径
	if c.Cache.Path == "" || !filepath.IsAbs(c.Cache.Path) {
		c.Cache.Path = filepath.Join(c.DataDir(), "cache")
	}

	// 4. MCP
	defaultMCP := mcp.DefaultConfig()
	c.MCP.Enabled = true
	c.MCP.LogLevel = defaultMCP.LogLevel
	c.MCP.DefaultTimeout = defaultMCP.DefaultTimeout
	c.MCP.MaxConcurrentRequests = defaultMCP.MaxConcurrentRequests
	c.MCP.HealthCheckInterval = defaultMCP.HealthCheckInterval
	c.MCP.Servers = nil
	c.MCP.ServersConfigPath = ""
	c.MCP.FilesystemIgnore = append([]string(nil), defaultMCP.FilesystemIgnore...)
	c.MCP.FilesystemDirs = []string{c.WorkspaceDir()}
	c.resolveMCPServerPaths()

	// 5. Skills
	c.Skills.Paths = nil
	c.Skills.AutoLoad = true
	c.Skills.AllowCommandInjection = false
	c.Skills.RequireConfirmation = true

	// 6. 确保目录结构
	c.ensureLayout()
}

func (c *Config) ensureLayout() {
	dirs := []string{c.DataDir(), c.ConfigDir(), c.SkillsDir(), c.IntentsDir(), c.WorkspaceDir()}
	for _, d := range dirs {
		_ = os.MkdirAll(d, 0755)
	}
	ensureParentDir(c.AgentDBPath())
	ensureParentDir(c.CortexDBPath())
}

// --- 加载逻辑 ---

func Load() (*Config, error) {
	configLoadMu.Lock()
	defer configLoadMu.Unlock()

	// 1. 确定 AGENTGO_HOME
	home := os.Getenv("AGENTGO_HOME")
	if home == "" {
		home = "~/.agentgo"
	}
	home = expandHomePath(home)

	config := defaultConfig(home)
	config.ApplyHomeLayout()
	if err := config.LoadDBBackedRuntime(); err != nil {
		return nil, err
	}

	return config, nil
}

func defaultConfig(home string) *Config {
	cfg := &Config{
		Home:  home,
		Debug: GetEnvOrDefaultBool("DEBUG", false),
		Server: ServerConfig{
			Port: 7127,
			Host: "0.0.0.0",
		},
		RAG: RAGConfig{
			Enabled: false,
		},
		Memory: MemoryConfig{
			StoreType: MemoryStoreTypeFile,
		},
		Cache: CacheConfig{
			StoreType: "memory",
		},
		Tooling: ToolingConfig{
			EnableSearchTools: true,
			WebSearch: WebSearchConfig{
				Mode: "mcp",
			},
		},
		Agent: AgentConfig{
			Name: "AgentGo",
		},
	}
	return cfg
}

// --- 工具函数 ---

func (c *Config) resolveMCPServerPaths() {
	unifiedPath := filepath.Join(c.Home, "mcpServers.json")
	for _, s := range c.MCP.Servers {
		if s == unifiedPath {
			return
		}
	}
	c.MCP.Servers = append([]string{unifiedPath}, c.MCP.Servers...)
}

func expandHomePath(path string) string {
	if strings.HasPrefix(path, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, path[2:])
	}
	return path
}

func ensureParentDir(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
}

func (c *Config) resetDBBackedRuntime() {
	c.LLM = LLMConfig{}
	c.Debug = GetEnvOrDefaultBool("DEBUG", false)
	c.Server.Host = "0.0.0.0"
	c.Server.Port = 7127
	c.RAG.Enabled = false
	c.RAG.EmbeddingModel = ""
	c.RAG.Embedding = EmbeddingConfig{}
	c.Memory.StoreType = MemoryStoreTypeFile
	c.MCP.Servers = nil
	c.Skills.Paths = nil
}

// LoadDBBackedRuntime refreshes runtime settings from agentgo.db.
func (c *Config) LoadDBBackedRuntime() error {
	db, err := store.NewAgentGoDB(c.AgentDBPath())
	if err != nil {
		return fmt.Errorf("open agentgo db: %w", err)
	}
	defer db.Close()

	return c.LoadDBBackedRuntimeFrom(db)
}

// LoadDBBackedRuntimeFrom refreshes LLM and embedding runtime settings using an existing AgentGoDB handle.
func (c *Config) LoadDBBackedRuntimeFrom(db *store.AgentGoDB) error {
	if db == nil {
		return fmt.Errorf("agentgo db is required")
	}

	c.resetDBBackedRuntime()

	debugValue, err := ensureDBConfigValue(db, "debug", boolString(c.Debug))
	if err != nil {
		return err
	}
	serverHost, err := ensureDBConfigValue(db, "server.host", c.Server.Host)
	if err != nil {
		return err
	}
	serverPort, err := ensureDBConfigValue(db, "server.port", strconv.Itoa(c.Server.Port))
	if err != nil {
		return err
	}
	memoryStoreType, err := ensureDBConfigValue(db, "memory.store_type", string(c.Memory.StoreType))
	if err != nil {
		return err
	}
	c.Debug = debugValue == "true"
	c.Server.Host = strings.TrimSpace(serverHost)
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if parsedPort, parseErr := strconv.Atoi(serverPort); parseErr == nil && parsedPort > 0 {
		c.Server.Port = parsedPort
	}
	if setErr := c.SetMemoryStoreTypeString(memoryStoreType); setErr == nil {
		c.applyMemoryLayout()
	}

	llmStrategy, err := ensureDBConfigValue(db, "llm.strategy", string(pool.StrategyRoundRobin))
	if err != nil {
		return err
	}
	llmProviders, err := db.ListProviders()
	if err != nil {
		return fmt.Errorf("list llm providers: %w", err)
	}
	c.LLM.Strategy = pool.SelectionStrategy(llmStrategy)
	c.LLM.Providers = enabledLLMProviders(llmProviders)

	embeddingStrategy, err := ensureDBConfigValue(db, "embedding.strategy", string(pool.StrategyRoundRobin))
	if err != nil {
		return err
	}
	embeddingProviders, err := db.ListEmbeddingProviders()
	if err != nil {
		return fmt.Errorf("list embedding providers: %w", err)
	}
	embeddingModel, err := db.GetConfig("rag.embedding_model")
	if err != nil {
		embeddingModel = ""
		for _, provider := range embeddingProviders {
			if provider != nil && provider.Enabled {
				embeddingModel = strings.TrimSpace(provider.ModelName)
				if embeddingModel != "" {
					if saveErr := db.SaveConfig("rag.embedding_model", embeddingModel); saveErr != nil {
						return fmt.Errorf("backfill rag.embedding_model: %w", saveErr)
					}
				}
				break
			}
		}
	}
	c.RAG.EmbeddingModel = embeddingModel
	c.RAG.Enabled = strings.TrimSpace(embeddingModel) != ""
	c.RAG.Embedding.Strategy = pool.SelectionStrategy(embeddingStrategy)
	c.RAG.Embedding.Providers = enabledEmbeddingProviders(embeddingProviders)

	if paths, err := dbConfigStringSlice(db, "skills.paths"); err == nil {
		c.Skills.Paths = paths
	}
	if paths, err := dbConfigStringSlice(db, "mcp.paths"); err == nil {
		c.MCP.Servers = paths
		c.resolveMCPServerPaths()
	}

	return nil
}

func ensureDBConfigValue(db *store.AgentGoDB, key, fallback string) (string, error) {
	value, err := db.GetConfig(key)
	if err == nil {
		return value, nil
	}
	if err := db.SaveConfig(key, fallback); err != nil {
		return "", fmt.Errorf("save default config %s: %w", key, err)
	}
	return fallback, nil
}

func enabledLLMProviders(providers []*store.LLMProvider) []pool.Provider {
	result := make([]pool.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil && provider.Enabled {
			result = append(result, store.ToPoolProvider(provider))
		}
	}
	return result
}

func enabledEmbeddingProviders(providers []*store.EmbeddingProvider) []pool.Provider {
	result := make([]pool.Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil && provider.Enabled {
			result = append(result, store.ToPoolEmbeddingProvider(provider))
		}
	}
	return result
}

func dbConfigStringSlice(db *store.AgentGoDB, key string) ([]string, error) {
	value, err := db.GetConfig(key)
	if err != nil {
		return nil, err
	}
	var items []string
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	return items, nil
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func (c *Config) Validate() error {
	if c.Server.Port <= 0 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}
	if c.Server.Host == "" {
		return fmt.Errorf("server host cannot be empty")
	}
	if c.RAG.Enabled && c.RAG.EmbeddingModel == "" && len(c.RAG.Embedding.Providers) == 0 {
		return fmt.Errorf("embedding_model or rag.embedding.providers is required when RAG is enabled")
	}
	return nil
}

func (c *Config) SkillsPaths() []string {
	paths := []string{
		".skills",
		filepath.Join(c.Home, "skills"),
	}

	// Add ~/.agents/skills if exists
	if home, err := os.UserHomeDir(); err == nil {
		agentsSkills := filepath.Join(home, ".agents", "skills")
		if _, err := os.Stat(agentsSkills); err == nil {
			paths = append(paths, agentsSkills)

			// Also add subdirectories of ~/.agents/skills as they might contain individual skills
			if entries, err := os.ReadDir(agentsSkills); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						paths = append(paths, filepath.Join(agentsSkills, entry.Name()))
					}
				}
			}
		}
	}

	// Add user-defined paths from config
	for _, p := range c.Skills.Paths {
		expanded := expandHomePath(p)
		paths = append(paths, expanded)
	}

	return paths
}

// MCPServersPaths returns all paths to look for mcpServers.json files
func (c *Config) MCPServersPaths() []string {
	paths := []string{filepath.Join(c.Home, "mcpServers.json")}

	if home, err := os.UserHomeDir(); err == nil {
		// ~/.agents/mcpServers.json
		agentsMcp := filepath.Join(home, ".agents", "mcpServers.json")
		if _, err := os.Stat(agentsMcp); err == nil {
			paths = append(paths, agentsMcp)
		}

		// ~/.agentgo/mcpServers.json (legacy)
		oldHomeMcp := filepath.Join(home, ".agentgo", "mcpServers.json")
		if _, err := os.Stat(oldHomeMcp); err == nil {
			paths = append(paths, oldHomeMcp)
		}
	}

	return paths
}

// MCPServersPath returns the path to the MCP servers configuration file
func (c *Config) MCPServersPath() string {
	return filepath.Join(c.Home, "mcpServers.json")
}

func (c *Config) validateMCPConfig() error {
	return nil // Simplified for now
}

func (c *Config) validateCacheConfig() error {
	return nil // Simplified for now
}

func (c *Config) validateToolingConfig() error {
	return nil // Simplified for now
}

// LoadMCPConfig loads MCP configuration from specific paths (supports multiple)
func LoadMCPConfig(paths ...string) (*mcp.Config, error) {
	cfg := mcp.DefaultConfig()
	cfg.Enabled = true
	cfg.Servers = paths
	if err := cfg.LoadServersFromJSON(); err != nil {
		return nil, fmt.Errorf("failed to load MCP servers: %w", err)
	}
	return &cfg, nil
}

func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func GetEnvOrDefaultInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func GetEnvOrDefaultBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}
