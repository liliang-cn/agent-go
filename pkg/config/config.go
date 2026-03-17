package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/pkg/mcp"
	"github.com/liliang-cn/agent-go/pkg/pool"
	"github.com/spf13/viper"
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

	// Internal storage configs (not exposed to user directly)
	Internal struct {
		Storage CortexdbConfig `mapstructure:"-"`
	} `mapstructure:"-"`
}

type ServerConfig struct {
	Port        int      `mapstructure:"port"`
	Host        string   `mapstructure:"host"`
	EnableUI    bool     `mapstructure:"enable_ui"`
	CORSOrigins []string `mapstructure:"cors_origins"`
}

type LLMConfig struct {
	Enabled   bool                   `mapstructure:"enabled"`
	Strategy  pool.SelectionStrategy `mapstructure:"strategy"`
	Providers []pool.Provider        `mapstructure:"providers"`
}

// EmbeddingConfig holds dedicated embedding provider settings.
type EmbeddingConfig struct {
	Enabled   bool                   `mapstructure:"enabled"`
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
	Enabled               bool     `mapstructure:"enabled"`
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
	c.MCP.FilesystemDirs = []string{c.WorkspaceDir()}
	c.resolveMCPServerPaths()

	// 5. 确保目录结构
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

func Load(configPath string) (*Config, error) {
	configLoadMu.Lock()
	defer configLoadMu.Unlock()

	// 1. 确定 AGENTGO_HOME
	home := os.Getenv("AGENTGO_HOME")
	if home == "" {
		home = "~/.agentgo"
	}
	home = expandHomePath(home)

	// 2. 配置文件查找
	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("agentgo")
		viper.SetConfigType("toml")
		viper.AddConfigPath(".")
		viper.AddConfigPath(home)
		viper.AddConfigPath(filepath.Join(home, "config"))
	}

	setDefaults()
	bindEnvVars()

	_ = viper.ReadInConfig()
	config := &Config{}
	if err := viper.Unmarshal(config); err != nil {
		return nil, err
	}

	if config.Home == "" {
		config.Home = home
	}
	config.ApplyHomeLayout()

	// 处理 Provider 特殊解析
	if viper.IsSet("llm.providers") {
		var llm struct{ Providers []interface{} }
		viper.UnmarshalKey("llm", &llm)
		unmarshalProviders(llm.Providers, &config.LLM.Providers)
	}
	if viper.IsSet("rag.embedding.providers") {
		var emb struct{ Providers []interface{} }
		viper.UnmarshalKey("rag.embedding", &emb)
		unmarshalProviders(emb.Providers, &config.RAG.Embedding.Providers)
	}

	return config, nil
}

func setDefaults() {
	viper.SetDefault("server.port", 7127)
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("llm.enabled", true)
	viper.SetDefault("llm.strategy", "round_robin")
	viper.SetDefault("rag.enabled", false)
	viper.SetDefault("skills.enabled", true)
	viper.SetDefault("memory.store_type", string(MemoryStoreTypeFile))
	viper.SetDefault("cache.store_type", "memory")
	viper.SetDefault("tooling.enable_search_tools", true)
	viper.SetDefault("tooling.web_search.mode", "mcp")
}

func bindEnvVars() {
	viper.SetEnvPrefix("AGENTGO")
	viper.AutomaticEnv()
	viper.BindEnv("home", "AGENTGO_HOME")
	viper.BindEnv("debug", "DEBUG")
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

func unmarshalProviders(raw interface{}, target *[]pool.Provider) {
	data, _ := json.Marshal(raw)
	_ = json.Unmarshal(data, target)
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
	paths := []string{".skills", filepath.Join(c.Home, "skills")}

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
