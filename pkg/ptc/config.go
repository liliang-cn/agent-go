package ptc

import (
	"time"
)

// Config represents PTC configuration
type Config struct {
	// Enabled enables or disables PTC functionality
	Enabled bool `mapstructure:"enabled" json:"enabled"`
	// DefaultRuntime is the default sandbox runtime (goja)
	DefaultRuntime RuntimeType `mapstructure:"default_runtime" json:"default_runtime"`
	// DefaultTimeout is the default execution timeout
	DefaultTimeout time.Duration `mapstructure:"default_timeout" json:"default_timeout"`
	// MaxMemoryMB is the maximum memory per execution in megabytes
	MaxMemoryMB int `mapstructure:"max_memory_mb" json:"max_memory_mb"`
	// MaxCodeSize is the maximum code size in bytes
	MaxCodeSize int `mapstructure:"max_code_size" json:"max_code_size"`
	// MaxOutputSize is the maximum output size in bytes
	MaxOutputSize int `mapstructure:"max_output_size" json:"max_output_size"`
	// MaxToolCalls is the maximum number of tool calls per execution
	MaxToolCalls int `mapstructure:"max_tool_calls" json:"max_tool_calls"`
	// GRPC contains gRPC configuration
	GRPC GRPCConfig `mapstructure:"grpc" json:"grpc"`
	// Security contains security settings
	Security SecurityConfig `mapstructure:"security" json:"security"`
	// History contains execution history settings
	History HistoryConfig `mapstructure:"history" json:"history"`
}

// GRPCConfig contains gRPC server configuration
type GRPCConfig struct {
	// Enabled enables the gRPC server
	Enabled bool `mapstructure:"enabled" json:"enabled"`
	// Address is the listen address (e.g., "unix:///tmp/ptc.sock" or ":50051")
	Address string `mapstructure:"address" json:"address"`
	// MaxRecvMsgSize is the maximum receive message size in bytes
	MaxRecvMsgSize int `mapstructure:"max_recv_msg_size" json:"max_recv_msg_size"`
	// MaxSendMsgSize is the maximum send message size in bytes
	MaxSendMsgSize int `mapstructure:"max_send_msg_size" json:"max_send_msg_size"`
}

// SecurityConfig contains security settings
type SecurityConfig struct {
	// AllowFileAccess allows file system access in sandbox
	AllowFileAccess bool `mapstructure:"allow_file_access" json:"allow_file_access"`
	// AllowNetwork allows network access in sandbox
	AllowNetwork bool `mapstructure:"allow_network" json:"allow_network"`
	// AllowedTools is a whitelist of allowed tools (empty = all)
	AllowedTools []string `mapstructure:"allowed_tools" json:"allowed_tools"`
	// BlockedTools is a blacklist of blocked tools
	BlockedTools []string `mapstructure:"blocked_tools" json:"blocked_tools"`
	// ValidateCode enables code validation before execution
	ValidateCode bool `mapstructure:"validate_code" json:"validate_code"`
	// ForbiddenPatterns are regex patterns for forbidden code constructs
	ForbiddenPatterns []string `mapstructure:"forbidden_patterns" json:"forbidden_patterns"`
}

// HistoryConfig contains execution history settings
type HistoryConfig struct {
	// Enabled enables execution history storage
	Enabled bool `mapstructure:"enabled" json:"enabled"`
	// MaxEntries is the maximum number of history entries to keep
	MaxEntries int `mapstructure:"max_entries" json:"max_entries"`
	// Retention is how long to keep history entries
	Retention time.Duration `mapstructure:"retention" json:"retention"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		DefaultRuntime: RuntimeGoja, // Start with Goja as it's simpler
		DefaultTimeout: 30 * time.Second,
		MaxMemoryMB:    64,
		MaxCodeSize:    64 * 1024,   // 64KB
		MaxOutputSize:  1024 * 1024, // 1MB
		MaxToolCalls:   20,
		GRPC: GRPCConfig{
			Enabled:        false,
			Address:        "unix:///tmp/ptc.sock",
			MaxRecvMsgSize: 4 * 1024 * 1024, // 4MB
			MaxSendMsgSize: 4 * 1024 * 1024, // 4MB
		},
		Security: SecurityConfig{
			AllowFileAccess: false,
			AllowNetwork:    false,
			AllowedTools:    []string{},
			BlockedTools:    []string{},
			ValidateCode:    true,
			ForbiddenPatterns: []string{
				`eval\s*\(`,
				`Function\s*\(`,
				`import\s*\(`,
				`require\s*\(`,
				`process\s*\.`,
				`global\s*\.`,
				`__proto__`,
			},
		},
		History: HistoryConfig{
			Enabled:    true,
			MaxEntries: 1000,
			Retention:  24 * time.Hour,
		},
	}
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.DefaultTimeout <= 0 {
		c.DefaultTimeout = 30 * time.Second
	}
	if c.MaxMemoryMB <= 0 {
		c.MaxMemoryMB = 64
	}
	if c.MaxCodeSize <= 0 {
		c.MaxCodeSize = 64 * 1024
	}
	if c.MaxOutputSize <= 0 {
		c.MaxOutputSize = 1024 * 1024
	}
	if c.MaxToolCalls <= 0 {
		c.MaxToolCalls = 20
	}
	if c.History.MaxEntries <= 0 {
		c.History.MaxEntries = 1000
	}
	if c.History.Retention <= 0 {
		c.History.Retention = 24 * time.Hour
	}
	return nil
}

// IsToolAllowed checks if a tool is allowed by security settings
func (c *Config) IsToolAllowed(toolName string) bool {
	// Check blocked list first
	for _, blocked := range c.Security.BlockedTools {
		if blocked == toolName || blocked == "*" {
			return false
		}
	}

	// If allowed list is empty, all tools are allowed (except blocked)
	if len(c.Security.AllowedTools) == 0 {
		return true
	}

	// Check allowed list
	for _, allowed := range c.Security.AllowedTools {
		if allowed == toolName || allowed == "*" {
			return true
		}
	}

	return false
}
