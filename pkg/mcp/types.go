package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	agentgolog "github.com/liliang-cn/agent-go/v2/pkg/log"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerType represents the type of MCP server connection
type ServerType string

const (
	ServerTypeStdio     ServerType = "stdio"
	ServerTypeHTTP      ServerType = "http"
	ServerTypeSSE       ServerType = "sse"
	ServerTypeInProcess ServerType = "inprocess"
)

// ServerConfig represents the configuration for an MCP server
type ServerConfig struct {
	Name             string            `toml:"name" json:"name" mapstructure:"name"`
	Description      string            `toml:"description" json:"description" mapstructure:"description"`
	Type             ServerType        `toml:"type" json:"type" mapstructure:"type"`                      // "stdio", "http", or "sse"
	Command          []string          `toml:"command" json:"command" mapstructure:"command"`             // For stdio type
	Args             []string          `toml:"args" json:"args" mapstructure:"args"`                      // For stdio type
	URL              string            `toml:"url" json:"url" mapstructure:"url"`                         // For http type
	Headers          map[string]string `toml:"headers" json:"headers" mapstructure:"headers"`             // For http type
	WorkingDir       string            `toml:"working_dir" json:"working_dir" mapstructure:"working_dir"` // For stdio type
	Env              map[string]string `toml:"env" json:"env" mapstructure:"env"`                         // For stdio type
	AutoStart        bool              `toml:"auto_start" json:"auto_start" mapstructure:"auto_start"`
	RestartOnFailure bool              `toml:"restart_on_failure" json:"restart_on_failure" mapstructure:"restart_on_failure"`
	MaxRestarts      int               `toml:"max_restarts" json:"max_restarts" mapstructure:"max_restarts"`
	RestartDelay     time.Duration     `toml:"restart_delay" json:"restart_delay" mapstructure:"restart_delay"`
	DefaultTimeout   time.Duration     `toml:"default_timeout" json:"default_timeout" mapstructure:"default_timeout"` // Timeout for initialize handshake
	Capabilities     []string          `toml:"capabilities" json:"capabilities" mapstructure:"capabilities"`
}

// ClientOptions configures the MCP client behavior
type ClientOptions struct {
	// Sampling - allows server to request LLM completions through the client
	CreateMessageHandler func(ctx context.Context, req *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error)

	// Elicitation - allows server to request information from users
	ElicitationHandler func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error)

	// Logging - receive log messages from server
	LoggingMessageHandler func(ctx context.Context, req *mcp.LoggingMessageRequest)

	// Roots - filesystem boundaries for server operations
	Roots []*mcp.Root
}

// Config represents the overall MCP configuration
type Config struct {
	Enabled               bool           `toml:"enabled" json:"enabled" mapstructure:"enabled"`
	LogLevel              string         `toml:"log_level" json:"log_level" mapstructure:"log_level"`
	DefaultTimeout        time.Duration  `toml:"default_timeout" json:"default_timeout" mapstructure:"default_timeout"`
	MaxConcurrentRequests int            `toml:"max_concurrent_requests" json:"max_concurrent_requests" mapstructure:"max_concurrent_requests"`
	HealthCheckInterval   time.Duration  `toml:"health_check_interval" json:"health_check_interval" mapstructure:"health_check_interval"`
	Servers               []string       `toml:"servers" json:"servers" mapstructure:"servers"`                                     // Array of server config file paths
	ServersConfigPath     string         `toml:"servers_config_path" json:"servers_config_path" mapstructure:"servers_config_path"` // Deprecated: use Servers instead
	FilesystemDirs        []string       `toml:"filesystem_dirs" json:"filesystem_dirs" mapstructure:"filesystem_dirs"`             // Allowed dirs for built-in filesystem server; defaults to home dir
	FilesystemIgnore      []string       `toml:"filesystem_ignore" json:"filesystem_ignore" mapstructure:"filesystem_ignore"`       // Blacklisted directory names filtered from filesystem MCP operations
	LoadedServers         []ServerConfig `toml:"-" json:"-" mapstructure:"-"`                                                       // Internal: loaded server configurations
	mu                    sync.Mutex     `toml:"-" json:"-" mapstructure:"-"`                                                       // Protects LoadedServers
}

// DefaultConfig returns default MCP configuration
func DefaultConfig() Config {
	return Config{
		Enabled:               false, // Start disabled by default
		LogLevel:              "info",
		DefaultTimeout:        30 * time.Second,
		MaxConcurrentRequests: 10,
		HealthCheckInterval:   60 * time.Second,
		Servers:               []string{}, // Empty by default, resolved by resolveMCPServerPaths()
		ServersConfigPath:     "",         // Deprecated
		FilesystemIgnore:      DefaultFilesystemIgnoreNames(),
		LoadedServers:         []ServerConfig{},
	}
}

// GetBuiltInServers returns the list of built-in in-process MCP servers.
// Both servers are compiled into the binary — no external commands needed.
// filesystemDirs sets the allowed directories for the filesystem server;
// if nil or empty, the user's home directory is used as the default root.
func GetBuiltInServers(filesystemDirs []string) []ServerConfig {
	if len(filesystemDirs) == 0 {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "/"
		}
		filesystemDirs = []string{homeDir}
	}
	return []ServerConfig{
		{
			Name:        "filesystem",
			Description: "Built-in filesystem MCP server (in-process, no external binary needed)",
			Type:        ServerTypeInProcess,
			Args:        filesystemDirs,
			AutoStart:   true,
		},
		{
			Name:        "websearch",
			Description: "Built-in web search MCP server (in-process, no external binary needed)",
			Type:        ServerTypeInProcess,
			Args:        []string{},
			AutoStart:   true,
		},
	}
}

// SimpleServerConfig represents a simplified server configuration for JSON files
type SimpleServerConfig struct {
	Type       string            `json:"type,omitempty"`        // "stdio", "http", or "sse"; defaults to "stdio"
	Command    string            `json:"command,omitempty"`     // For stdio type
	Args       []string          `json:"args,omitempty"`        // For stdio type
	URL        string            `json:"url,omitempty"`         // For http type
	Headers    map[string]string `json:"headers,omitempty"`     // For http type
	WorkingDir string            `json:"working_dir,omitempty"` // For stdio type
	Env        map[string]string `json:"env,omitempty"`         // For stdio type
}

// JSONServersConfig represents the root structure of the JSON MCP servers config file
type JSONServersConfig struct {
	MCPServers map[string]SimpleServerConfig `json:"mcpServers"`
}

// LoadServersFromJSON loads MCP server configurations from JSON files
func (c *Config) LoadServersFromJSON() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Preserve only built-in servers when reloading; clear any previously loaded
	// external servers so repeated loads are idempotent.
	builtinServers := filterBuiltInServers(c.LoadedServers)

	// Clear loaded servers
	c.LoadedServers = []ServerConfig{}

	// Add back built-in servers first
	c.LoadedServers = append(c.LoadedServers, builtinServers...)

	// Load from new Servers array
	for _, serverFile := range c.Servers {
		if err := c.loadServerFile(serverFile); err != nil {
			// Skip files that don't exist, fail on other errors
			if errors.Is(err, os.ErrNotExist) {
				agentgolog.Debugf("MCP config file not found, skipping: %s", serverFile)
				continue
			}
			return fmt.Errorf("failed to load server file %s: %w", serverFile, err)
		}
	}

	// Backward compatibility: also load from ServersConfigPath if set
	if c.ServersConfigPath != "" {
		if err := c.loadServerFile(c.ServersConfigPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				agentgolog.Debugf("MCP config file not found, skipping: %s", c.ServersConfigPath)
			} else {
				return fmt.Errorf("failed to load server file %s: %w", c.ServersConfigPath, err)
			}
		}
	}

	return nil
}

func filterBuiltInServers(servers []ServerConfig) []ServerConfig {
	filtered := make([]ServerConfig, 0, len(servers))
	for _, server := range servers {
		if server.Type == ServerTypeInProcess {
			filtered = append(filtered, server)
		}
	}
	return filtered
}

// GetLoadedServers returns a copy of loaded servers with thread-safe access
func (c *Config) GetLoadedServers() []ServerConfig {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return a copy to prevent external modification
	servers := make([]ServerConfig, len(c.LoadedServers))
	copy(servers, c.LoadedServers)
	return servers
}

// AddServer adds a server configuration to the loaded servers list
func (c *Config) AddServer(serverConfig *ServerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LoadedServers = append(c.LoadedServers, *serverConfig)
}

// loadServerFile loads a single server configuration file
func (c *Config) loadServerFile(serverFile string) error {
	configPath := serverFile

	// Read the JSON file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read MCP servers config file %s: %w", configPath, err)
	}

	// Parse JSON
	var jsonConfig JSONServersConfig
	if err := json.Unmarshal(data, &jsonConfig); err != nil {
		return fmt.Errorf("failed to parse MCP servers config JSON: %w", err)
	}

	// Convert to ServerConfig and append to existing servers
	for name, simpleConfig := range jsonConfig.MCPServers {
		// Determine server type
		serverType := ServerTypeStdio
		if simpleConfig.Type != "" {
			serverType = ServerType(simpleConfig.Type)
		} else if simpleConfig.URL != "" {
			// Auto-detect HTTP type if URL is provided
			serverType = ServerTypeHTTP
		}

		serverConfig := ServerConfig{
			Name:             name,
			Description:      fmt.Sprintf("MCP server: %s", name),
			Type:             serverType,
			Command:          []string{},
			Args:             simpleConfig.Args,
			URL:              simpleConfig.URL,
			Headers:          simpleConfig.Headers,
			WorkingDir:       simpleConfig.WorkingDir,
			Env:              simpleConfig.Env,
			AutoStart:        true, // Default to auto-start for JSON-configured servers
			RestartOnFailure: true,
			MaxRestarts:      3,
			RestartDelay:     5 * time.Second,
			Capabilities:     []string{}, // Will be discovered at runtime
		}

		// Set command based on type
		if serverType == ServerTypeStdio && simpleConfig.Command != "" {
			serverConfig.Command = []string{simpleConfig.Command}
		}

		// Add to loaded servers list
		c.LoadedServers = append(c.LoadedServers, serverConfig)
	}

	return nil
}

// Client represents an MCP client connection to a server
type Client struct {
	config            *ServerConfig
	session           *mcp.ClientSession
	tools             map[string]*mcp.Tool
	resources         map[string]*mcp.Resource
	resourceTemplates map[string]*mcp.ResourceTemplate
	prompts           map[string]*mcp.Prompt
	connected         bool

	// Health monitoring
	cmd             *exec.Cmd     // Process for Stdio servers
	stopHealthCheck chan struct{} // Channel to stop health check goroutine
	mu              sync.Mutex    // Protects connected state

	// SDK client (for adding roots dynamically)
	mcpClient *mcp.Client
	options   *ClientOptions
}

// ToolInfo represents information about an MCP tool
type ToolInfo struct {
	ServerName  string                 `json:"server_name"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	LastUsed    time.Time              `json:"last_used"`
	UsageCount  int64                  `json:"usage_count"`
}

// ToolResult represents the result of an MCP tool call
type ToolResult struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Error   string      `json:"error,omitempty"`
}

// ResourceInfo represents information about an MCP resource
type ResourceInfo struct {
	ServerName  string                 `json:"server_name"`
	URI         string                 `json:"uri"`
	Name        string                 `json:"name"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description"`
	MIMEType    string                 `json:"mime_type,omitempty"`
	Annotations map[string]interface{} `json:"annotations,omitempty"`
}

// ResourceTemplateInfo represents information about an MCP resource template
type ResourceTemplateInfo struct {
	ServerName  string                 `json:"server_name"`
	URITemplate string                 `json:"uri_template"`
	Name        string                 `json:"name"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description"`
	MIMEType    string                 `json:"mime_type,omitempty"`
	Annotations map[string]interface{} `json:"annotations,omitempty"`
}

// ResourceContent represents the content of a read resource
type ResourceContent struct {
	URI      string      `json:"uri"`
	MIMEType string      `json:"mime_type,omitempty"`
	Content  interface{} `json:"content"`
}

// PromptInfo represents information about an MCP prompt
type PromptInfo struct {
	ServerName  string               `json:"server_name"`
	Name        string               `json:"name"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description"`
	Arguments   []PromptArgumentInfo `json:"arguments,omitempty"`
}

// PromptArgumentInfo represents an argument for an MCP prompt
type PromptArgumentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

// PromptContent represents the content of a retrieved prompt
type PromptContent struct {
	ServerName  string              `json:"server_name"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Messages    []PromptMessageInfo `json:"messages"`
}

// PromptMessageInfo represents a message in a prompt
type PromptMessageInfo struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}
