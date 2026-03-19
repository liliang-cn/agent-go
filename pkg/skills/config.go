package skills

import (
	"os"
	"path/filepath"
)

// Config configures the skills module
type Config struct {
	Enabled      bool     `json:"enabled"`
	Paths        []string `json:"paths"`     // Paths to search for skills
	AutoLoad     bool     `json:"auto_load"` // Auto-load skills on startup
	CacheEnabled bool     `json:"cache_enabled"`
	DBPath       string   `json:"db_path"` // Path to skills database
	LogLevel     string   `json:"log_level"`

	// Security
	AllowCommandInjection bool `json:"allow_command_injection"`
	RequireConfirmation   bool `json:"require_confirmation"`

	// Integration
	EnableRAGIntegration   bool `json:"enable_rag_integration"`
	EnableMCPIntegration   bool `json:"enable_mcp_integration"`
	EnableAgentIntegration bool `json:"enable_agent_integration"`
}

// DefaultConfig returns default skills configuration
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	agentsSkills := filepath.Join(homeDir, ".agents", "skills")
	localSkills := "./.agentgo/skills"
	userSkills := filepath.Join(homeDir, ".agentgo", "skills")

	return &Config{
		Enabled:                true,
		Paths:                  []string{".skills", agentsSkills, localSkills, userSkills},
		AutoLoad:               true,
		CacheEnabled:           true,
		DBPath:                 filepath.Join(homeDir, ".agentgo", "data", "agentgo.db"),
		LogLevel:               "info",
		AllowCommandInjection:  false,
		RequireConfirmation:    true,
		EnableRAGIntegration:   true,
		EnableMCPIntegration:   true,
		EnableAgentIntegration: true,
	}
}

// LoadConfig returns the skills config from a map (used when loading from main config)
func LoadConfig(m map[string]interface{}) *Config {
	cfg := DefaultConfig()

	if enabled, ok := m["enabled"].(bool); ok {
		cfg.Enabled = enabled
	}
	if paths, ok := m["paths"].([]interface{}); ok {
		cfg.Paths = make([]string, 0, len(paths))
		for _, p := range paths {
			if s, ok := p.(string); ok {
				cfg.Paths = append(cfg.Paths, s)
			}
		}
	}
	if autoLoad, ok := m["auto_load"].(bool); ok {
		cfg.AutoLoad = autoLoad
	}
	if dbPath, ok := m["db_path"].(string); ok {
		cfg.DBPath = dbPath
	}
	if allowCommandInjection, ok := m["allow_command_injection"].(bool); ok {
		cfg.AllowCommandInjection = allowCommandInjection
	}
	if requireConfirmation, ok := m["require_confirmation"].(bool); ok {
		cfg.RequireConfirmation = requireConfirmation
	}

	return cfg
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Paths) == 0 {
		c.Paths = []string{".skills"}
	}
	if c.DBPath == "" {
		homeDir, _ := os.UserHomeDir()
		c.DBPath = filepath.Join(homeDir, ".agentgo", "data", "agentgo.db")
	}
	return nil
}
