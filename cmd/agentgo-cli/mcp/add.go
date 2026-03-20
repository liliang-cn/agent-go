package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/spf13/cobra"
)

// serverConfigJSON represents the JSON structure for mcpServers.json
type serverConfigJSON struct {
	MCPServers map[string]serverConfig `json:"mcpServers"`
}

// serverConfig represents a single server configuration in JSON
type serverConfig struct {
	Type       string            `json:"type,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// mcpAddCmd adds a new MCP server configuration
var mcpAddCmd = &cobra.Command{
	Use:   "add <server-name> <command>",
	Short: "Add a new MCP server configuration",
	Long: `Add a new MCP server to the configuration.

The server will be added to mcpServers.json and can be used immediately.

Examples:
  # Add a Node.js server via npx
  agentgo mcp add filesystem "npx -y @modelcontextprotocol/server-filesystem /path/to/allowed"

  # Add a Python server via uvx
  agentgo mcp add fetch "uvx mcp-server-fetch"

  # Add with specific arguments
  agentgo mcp add my-server "python server.py" --arg "--debug" --desc "My custom server"`,
	Args: cobra.MinimumNArgs(2),
	RunE: runMCPAdd,
}

// mcpAddJSONCmd adds a new MCP server from JSON configuration
var mcpAddJSONCmd = &cobra.Command{
	Use:   "add-json <server-name> <json-config>",
	Short: "Add a new MCP server from JSON configuration",
	Long: `Add a new MCP server to the configuration using JSON.

This allows you to add servers with full configuration including headers,
environment variables, and other options.

Examples:
  # Add an HTTP server with authentication
  agentgo mcp add-json github '{"type":"http","url":"https://api.github.com/mcp","headers":{"Authorization":"Bearer YOUR_TOKEN"}}'

  # Add a stdio server with environment variables
  agentgo mcp add-json myserver '{"type":"stdio","command":"node","args":["server.js"],"env":{"DEBUG":"true"}}'

  # Add a server with working directory
  agentgo mcp add-json local '{"command":"./server","working_dir":"/path/to/project"}'`,
	Args: cobra.ExactArgs(2),
	RunE: runMCPAddJSON,
}

// mcpRemoveCmd removes an MCP server configuration
var mcpRemoveCmd = &cobra.Command{
	Use:   "remove <server-name>",
	Short: "Remove an MCP server configuration",
	Long: `Remove an MCP server from the configuration.

The server will be removed from mcpServers.json.

Examples:
  agentgo mcp remove filesystem`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPRemove,
}

var (
	addDescription string
	addURL         string
	addArgs        []string
)

func init() {
	MCPCmd.AddCommand(mcpAddCmd)
	MCPCmd.AddCommand(mcpAddJSONCmd)
	MCPCmd.AddCommand(mcpRemoveCmd)

	// Add flags (avoid -d which conflicts with global --debug)
	mcpAddCmd.Flags().StringVar(&addDescription, "description", "", "Server description")
	mcpAddCmd.Flags().StringVarP(&addURL, "url", "u", "", "HTTP URL (for HTTP-type servers)")
	mcpAddCmd.Flags().StringSliceVar(&addArgs, "arg", []string{}, "Additional command arguments")
}

func runMCPAdd(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load("")
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	serverName := args[0]
	command := args[1]

	// Additional positional arguments are treated as args
	positionalArgs := []string{}
	if len(args) > 2 {
		positionalArgs = args[2:]
	}

	// Combine positional args and flag args
	allArgs := append(positionalArgs, addArgs...)

	// Determine the mcpServers.json file path
	configFile := getConfigFilePath()

	// Load existing config
	var jsonConfig serverConfigJSON
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &jsonConfig); err != nil {
			return fmt.Errorf("failed to parse existing config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Check if server already exists
	if _, exists := jsonConfig.MCPServers[serverName]; exists {
		return fmt.Errorf("server %s already exists. Use 'agentgo mcp remove %s' first", serverName, serverName)
	}

	// Create new server config
	serverCfg := serverConfig{
		Type:    "stdio",
		Command: command,
		Args:    allArgs,
	}

	// Set URL if provided
	if addURL != "" {
		serverCfg.Type = "http"
		serverCfg.URL = addURL
		serverCfg.Command = "" // Clear command for HTTP type
	}

	// Add to config
	if jsonConfig.MCPServers == nil {
		jsonConfig.MCPServers = make(map[string]serverConfig)
	}
	jsonConfig.MCPServers[serverName] = serverCfg

	// Write back to file
	if err := saveConfigFile(configFile, &jsonConfig); err != nil {
		return err
	}

	fmt.Printf("✅ Added MCP server: %s\n", serverName)
	fmt.Printf("   Type: %s\n", serverCfg.Type)
	if serverCfg.Type == "stdio" {
		fmt.Printf("   Command: %s\n", command)
		if len(allArgs) > 0 {
			fmt.Printf("   Args: %v\n", allArgs)
		}
	} else {
		fmt.Printf("   URL: %s\n", serverCfg.URL)
	}
	fmt.Printf("   Config: %s\n\n", configFile)
	fmt.Printf("💡 Test with: agentgo mcp list\n")

	return nil
}

func runMCPAddJSON(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load("")
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	serverName := args[0]
	jsonConfigStr := args[1]

	// Parse the JSON configuration
	var serverCfg serverConfig
	if err := json.Unmarshal([]byte(jsonConfigStr), &serverCfg); err != nil {
		return fmt.Errorf("failed to parse JSON config: %w", err)
	}

	// Set default type if not specified
	if serverCfg.Type == "" {
		if serverCfg.URL != "" {
			serverCfg.Type = "http"
		} else {
			serverCfg.Type = "stdio"
		}
	}

	// Validate configuration
	if serverCfg.Type == "http" && serverCfg.URL == "" {
		return fmt.Errorf("HTTP server requires 'url' field")
	}
	if serverCfg.Type == "stdio" && serverCfg.Command == "" {
		return fmt.Errorf("stdio server requires 'command' field")
	}

	// Determine the mcpServers.json file path
	configFile := getConfigFilePath()

	// Load existing config
	var jsonConfig serverConfigJSON
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &jsonConfig); err != nil {
			return fmt.Errorf("failed to parse existing config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Check if server already exists
	if _, exists := jsonConfig.MCPServers[serverName]; exists {
		return fmt.Errorf("server %s already exists. Use 'agentgo mcp remove %s' first", serverName, serverName)
	}

	// Add to config
	if jsonConfig.MCPServers == nil {
		jsonConfig.MCPServers = make(map[string]serverConfig)
	}
	jsonConfig.MCPServers[serverName] = serverCfg

	// Write back to file
	if err := saveConfigFile(configFile, &jsonConfig); err != nil {
		return err
	}

	fmt.Printf("✅ Added MCP server: %s\n", serverName)
	fmt.Printf("   Type: %s\n", serverCfg.Type)
	if serverCfg.Type == "stdio" {
		fmt.Printf("   Command: %s\n", serverCfg.Command)
		if len(serverCfg.Args) > 0 {
			fmt.Printf("   Args: %v\n", serverCfg.Args)
		}
		if serverCfg.WorkingDir != "" {
			fmt.Printf("   Working Dir: %s\n", serverCfg.WorkingDir)
		}
		if len(serverCfg.Env) > 0 {
			fmt.Printf("   Env: %v\n", serverCfg.Env)
		}
	} else {
		fmt.Printf("   URL: %s\n", serverCfg.URL)
		if len(serverCfg.Headers) > 0 {
			fmt.Printf("   Headers: %v\n", serverCfg.Headers)
		}
	}
	fmt.Printf("   Config: %s\n\n", configFile)
	fmt.Printf("💡 Test with: agentgo mcp list\n")

	return nil
}

func runMCPRemove(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load("")
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	serverName := args[0]

	// Determine the mcpServers.json file path
	configFile := getConfigFilePath()

	// Load existing config
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", configFile)
		}
		return fmt.Errorf("failed to read config: %w", err)
	}

	var jsonConfig serverConfigJSON
	if err := json.Unmarshal(data, &jsonConfig); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Check if server exists
	if _, exists := jsonConfig.MCPServers[serverName]; !exists {
		return fmt.Errorf("server %s not found in configuration", serverName)
	}

	// Remove server
	delete(jsonConfig.MCPServers, serverName)

	// Write back to file
	if err := saveConfigFile(configFile, &jsonConfig); err != nil {
		return err
	}

	fmt.Printf("✅ Removed MCP server: %s\n", serverName)
	fmt.Printf("   Config: %s\n\n", configFile)

	return nil
}

// getConfigFilePath determines the appropriate mcpServers.json file path
func getConfigFilePath() string {
	// 1. Check if local mcpServers.json exists
	configFile := "./mcpServers.json"
	if _, err := os.Stat(configFile); err == nil {
		return configFile
	}

	// 2. Use unified path from config
	if Cfg != nil {
		configFile = Cfg.MCPServersPath()
		if _, err := os.Stat(configFile); err == nil {
			return configFile
		}
	}

	// 3. Check old ~/.agentgo/mcpServers.json
	homeDir, _ := os.UserHomeDir()
	oldHomeConfig := filepath.Join(homeDir, ".agentgo", "mcpServers.json")
	if _, err := os.Stat(oldHomeConfig); err == nil {
		return oldHomeConfig
	}

	// 4. Default to unified path (will be created)
	if Cfg != nil {
		return Cfg.MCPServersPath()
	}

	// 5. Final fallback
	return filepath.Join(homeDir, ".agentgo", "mcpServers.json")
}

// saveConfigFile saves the configuration to a JSON file
func saveConfigFile(configFile string, jsonConfig *serverConfigJSON) error {
	data, err := json.MarshalIndent(jsonConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(configFile)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// Helper function to format server config for display
func formatServerConfig(cfg serverConfig) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("type=%s", cfg.Type))

	if cfg.Type == "http" {
		parts = append(parts, fmt.Sprintf("url=%s", cfg.URL))
		if len(cfg.Headers) > 0 {
			headerKeys := make([]string, 0, len(cfg.Headers))
			for k := range cfg.Headers {
				headerKeys = append(headerKeys, k)
			}
			parts = append(parts, fmt.Sprintf("headers=[%s]", strings.Join(headerKeys, ",")))
		}
	} else {
		parts = append(parts, fmt.Sprintf("command=%s", cfg.Command))
		if len(cfg.Args) > 0 {
			parts = append(parts, fmt.Sprintf("args=%v", cfg.Args))
		}
		if cfg.WorkingDir != "" {
			parts = append(parts, fmt.Sprintf("working_dir=%s", cfg.WorkingDir))
		}
		if len(cfg.Env) > 0 {
			envKeys := make([]string, 0, len(cfg.Env))
			for k := range cfg.Env {
				envKeys = append(envKeys, k)
			}
			parts = append(parts, fmt.Sprintf("env=[%s]", strings.Join(envKeys, ",")))
		}
	}

	return strings.Join(parts, ", ")
}
