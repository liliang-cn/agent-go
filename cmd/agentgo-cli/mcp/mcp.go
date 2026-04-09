package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/spf13/cobra"
)

// MCPCmd is the parent command for MCP operations - exported for use in root.go
var MCPCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP (Model Context Protocol) tool usage",
	Long: `Use MCP tools directly.

MCP enables agentgo to connect to external tools and services through the Model Context Protocol.
Servers are started on-demand when tools are called.`,
}

var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured MCP servers",
	RunE:  runMCPList,
}

var mcpCallCmd = &cobra.Command{
	Use:   "call <tool-name> [args...]",
	Short: "Call an MCP tool",
	Long: `Call an MCP tool with JSON arguments.

Examples:
  agentgo mcp call mcp_sqlite_query '{"query": "SELECT * FROM users LIMIT 5"}'
  agentgo mcp call mcp_filesystem_read '{"path": "./README.md"}'
  agentgo mcp call mcp_git_status '{}'`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMCPCall,
}

var mcpChatCmd = &cobra.Command{
	Use:   "chat [message]",
	Short: "Chat with MCP tools directly (no RAG)",
	Long: `Direct chat with MCP tools bypassing RAG search.

This command allows you to interact with MCP tools without any document search.
The AI will only use MCP tools to answer your questions.

Examples:
  # Single message
  agentgo mcp chat "Create a table called users with id, name, email columns"

  # Interactive mode (no message provided)
  agentgo mcp chat`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMCPChat,
}

var mcpCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check MCP runtime environment (npx, uvx availability)",
	Long: `Check if required tools for MCP servers are available.

This command checks for:
  - npx (Node.js package runner) - required for Node.js MCP servers
  - uvx (uv tool runner) - required for Python MCP servers

Examples:
  agentgo mcp check`,
	RunE: runMCPCheck,
}

func init() {
	// Add subcommands to MCP parent command
	MCPCmd.AddCommand(mcpListCmd)
	MCPCmd.AddCommand(mcpCallCmd)
	MCPCmd.AddCommand(mcpChatCmd)
	MCPCmd.AddCommand(mcpCheckCmd)
	// Note: mcpChatAdvancedCmd is commented out in mcp_chat.go to avoid duplicate

	// Add flags
	mcpListCmd.Flags().StringP("server", "s", "", "Filter servers by name")
	mcpListCmd.Flags().BoolP("json", "j", false, "Output in JSON format")
	mcpListCmd.Flags().BoolP("skip-failed", "k", true, "Deprecated: server listing no longer starts MCP processes")

	mcpCallCmd.Flags().StringP("timeout", "t", "30s", "Call timeout duration")
	mcpCallCmd.Flags().BoolP("json", "j", false, "Output result in JSON format")

	// Chat command flags
	mcpChatCmd.Flags().Float64P("temperature", "T", 0.7, "Generation temperature")
	mcpChatCmd.Flags().IntP("max-tokens", "m", 30000, "Maximum generation length")
	mcpChatCmd.Flags().BoolP("show-thinking", "t", true, "Show AI thinking process")
	mcpChatCmd.Flags().StringSliceP("allowed-tools", "a", []string{}, "Comma-separated list of allowed tools")
}

func runMCPCheck(cmd *cobra.Command, args []string) error {
	status := CheckMCPEnvironment()
	PrintMCPEnvironmentStatus(status)
	return nil
}

func runMCPChat(cmd *cobra.Command, args []string) error {
	// Get flags
	temperature, _ := cmd.Flags().GetFloat64("temperature")
	maxTokens, _ := cmd.Flags().GetInt("max-tokens")
	showThinking, _ := cmd.Flags().GetBool("show-thinking")
	allowedTools, _ := cmd.Flags().GetStringSlice("allowed-tools")

	// Check if interactive mode (no arguments provided)
	if len(args) == 0 {
		return fmt.Errorf("interactive mode not available - please provide a message")
	}

	message := strings.Join(args, " ")

	if Cfg == nil {
		var err error
		Cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	ctx := context.Background()

	// Get global LLM service
	llmService, err := services.GetGlobalLLM()
	if err != nil {
		return fmt.Errorf("failed to get global LLM service: %w", err)
	}

	// Create MCP tool manager
	mcpManager := mcp.NewMCPToolManager(&Cfg.MCP)

	// Ensure MCP servers are started (continue even if some fail)
	succeeded, failed := mcpManager.StartWithFailures(ctx)
	if len(failed) > 0 {
		fmt.Printf("⚠️  Warning: Failed to start %d server(s): %s\n", len(failed), strings.Join(failed, ", "))
	}
	if len(succeeded) == 0 {
		return fmt.Errorf("no MCP servers could be started - please check MCP server status")
	}

	// Wait a moment for servers to initialize
	time.Sleep(time.Second)

	// Get available tools
	toolsMap := mcpManager.ListTools()

	if len(toolsMap) == 0 {
		return fmt.Errorf("no MCP tools available - please check MCP server status")
	}

	fmt.Printf("🔧 Found %d MCP tools available\n", len(toolsMap))

	// Convert to slice for filtering
	var tools []*mcp.MCPToolWrapper
	for _, tool := range toolsMap {
		tools = append(tools, tool)
	}

	// Filter tools if allowed-tools is specified
	if len(allowedTools) > 0 {
		allowedSet := make(map[string]bool)
		for _, tool := range allowedTools {
			allowedSet[tool] = true
		}

		var filteredTools []*mcp.MCPToolWrapper
		for _, tool := range tools {
			if allowedSet[tool.Name()] {
				filteredTools = append(filteredTools, tool)
			}
		}
		tools = filteredTools
	}

	// Build tool definitions for LLM
	var toolDefinitions []domain.ToolDefinition
	for _, tool := range tools {
		definition := domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Schema(),
			},
		}
		toolDefinitions = append(toolDefinitions, definition)
	}

	// Prepare messages
	messages := []domain.Message{
		{
			Role:    "user",
			Content: message,
		},
	}

	// Generation options
	think := showThinking
	opts := &domain.GenerationOptions{
		Temperature: temperature,
		MaxTokens:   int(maxTokens),
		Think:       &think,
	}

	// Call LLM with tools
	result, err := llmService.GenerateWithTools(ctx, messages, toolDefinitions, opts)
	if err != nil {
		return fmt.Errorf("failed to generate response: %w", err)
	}

	// Show thinking if enabled
	if showThinking && result.Content != "" {
		// The thinking content might be included in the content
		fmt.Printf("🤔 **Thinking included in response**\n\n")
	}

	// Handle tool calls
	if len(result.ToolCalls) > 0 {
		fmt.Printf("🔧 **Tool Calls:**\n")

		var toolResults []domain.Message
		for _, toolCall := range result.ToolCalls {
			fmt.Printf("- Calling `%s`\n", toolCall.Function.Name)

			// Execute tool call via MCP
			result, err := mcpManager.CallTool(ctx, toolCall.Function.Name, toolCall.Function.Arguments)
			if err != nil {
				fmt.Printf("  ❌ Error: %v\n", err)
				continue
			}

			// Format result
			var resultStr string
			if result.Data != nil {
				resultStr = fmt.Sprintf("%v", result.Data)
			} else if result.Success {
				resultStr = "Tool executed successfully"
			} else {
				resultStr = fmt.Sprintf("Tool execution failed: %s", result.Error)
			}

			fmt.Printf("  ✅ Result: %s\n", resultStr)

			// Add tool result for potential follow-up
			toolResults = append(toolResults, domain.Message{
				Role:       "tool",
				Content:    resultStr,
				ToolCallID: toolCall.ID,
			})
		}

		// If we have tool results, send follow-up request for final response
		if len(toolResults) > 0 {
			// Append assistant message with tool calls
			messages = append(messages, domain.Message{
				Role:      "assistant",
				Content:   result.Content,
				ToolCalls: result.ToolCalls,
			})

			// Append tool results
			messages = append(messages, toolResults...)

			followUpResult, err := llmService.GenerateWithTools(ctx, messages, toolDefinitions, opts)
			if err == nil {
				fmt.Printf("\n💬 **Final Response:**\n%s\n", followUpResult.Content)
			}
		}
	} else {
		// Direct response without tools
		fmt.Printf("💬 **Response:**\n%s\n", result.Content)
	}

	return nil
}

func runMCPList(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	serverFilter, _ := cmd.Flags().GetString("server")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	rows, err := configuredMCPServerRows(Cfg, serverFilter)
	if err != nil {
		return err
	}

	if jsonOutput {
		output, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal server list: %w", err)
		}
		fmt.Println(string(output))
		return nil
	}

	if len(rows) == 0 {
		fmt.Println("No MCP servers configured.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Name\tCommand\tArgs\tEnv\tCwd\tStatus\tAuth")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name, row.Command, row.Args, row.Env, row.Cwd, row.Status, row.Auth)
	}
	w.Flush()

	return nil
}

type mcpListRow struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Args    string `json:"args"`
	Env     string `json:"env"`
	Cwd     string `json:"cwd"`
	Status  string `json:"status"`
	Auth    string `json:"auth"`
}

func configuredMCPServerRows(cfg *config.Config, serverFilter string) ([]mcpListRow, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	mcpCfg := mcp.Config{
		Enabled:               cfg.MCP.Enabled,
		LogLevel:              cfg.MCP.LogLevel,
		DefaultTimeout:        cfg.MCP.DefaultTimeout,
		MaxConcurrentRequests: cfg.MCP.MaxConcurrentRequests,
		HealthCheckInterval:   cfg.MCP.HealthCheckInterval,
		Servers:               append([]string(nil), cfg.MCP.Servers...),
		ServersConfigPath:     cfg.MCP.ServersConfigPath,
		FilesystemDirs:        append([]string(nil), cfg.MCP.FilesystemDirs...),
		FilesystemIgnore:      append([]string(nil), cfg.MCP.FilesystemIgnore...),
	}
	mcpCfg.LoadedServers = nil
	if err := mcpCfg.LoadServersFromJSON(); err != nil {
		return nil, fmt.Errorf("failed to load MCP server configurations: %w", err)
	}

	filter := strings.ToLower(strings.TrimSpace(serverFilter))
	loaded := mcpCfg.GetLoadedServers()
	rows := make([]mcpListRow, 0, len(loaded))
	for _, server := range loaded {
		if server.Type == mcp.ServerTypeInProcess {
			continue
		}
		if filter != "" && !strings.EqualFold(server.Name, filter) {
			continue
		}
		rows = append(rows, mcpListRow{
			Name:    strings.TrimSpace(server.Name),
			Command: formatMCPListCommand(server),
			Args:    formatMCPListArgs(server.Args),
			Env:     formatMCPListEnv(server.Env),
			Cwd:     dashIfEmpty(strings.TrimSpace(server.WorkingDir)),
			Status:  "enabled",
			Auth:    "Unsupported",
		})
	}
	slices.SortFunc(rows, func(a, b mcpListRow) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return rows, nil
}

func formatMCPListCommand(server mcp.ServerConfig) string {
	if server.Type == mcp.ServerTypeHTTP || server.Type == mcp.ServerTypeSSE {
		return dashIfEmpty(strings.TrimSpace(server.URL))
	}
	if len(server.Command) == 0 {
		return "-"
	}
	return strings.Join(server.Command, " ")
}

func formatMCPListArgs(args []string) string {
	if len(args) == 0 {
		return "-"
	}
	return strings.Join(args, " ")
}

func formatMCPListEnv(env map[string]string) string {
	if len(env) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

func dashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func runMCPCall(cmd *cobra.Command, args []string) error {
	if Cfg == nil {
		var err error
		Cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	toolName := args[0]
	timeoutStr, _ := cmd.Flags().GetString("timeout")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return fmt.Errorf("invalid timeout: %w", err)
	}

	// Parse arguments
	var toolArgs map[string]interface{}
	if len(args) > 1 {
		argsStr := strings.Join(args[1:], " ")
		if err := json.Unmarshal([]byte(argsStr), &toolArgs); err != nil {
			return fmt.Errorf("failed to parse arguments as JSON: %w", err)
		}
	} else {
		toolArgs = make(map[string]interface{})
	}

	toolManager := mcp.NewMCPToolManager(&Cfg.MCP)
	defer func() {
		if err := toolManager.Close(); err != nil {
			// Only print error if it's not a signal-related termination
			if !strings.Contains(err.Error(), "signal: killed") {
				fmt.Printf("Warning: failed to clean up tool manager: %v\n", err)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout+10*time.Second)
	defer cancel()

	// Start servers (continue even if some fail)
	succeeded, failed := toolManager.StartWithFailures(ctx)
	if len(failed) > 0 {
		fmt.Printf("⚠️  Warning: Failed to start %d server(s): %s\n", len(failed), strings.Join(failed, ", "))
		fmt.Printf("   Run 'agentgo mcp status --verbose' for more details\n")
	}
	if len(succeeded) == 0 {
		fmt.Printf("\n❌ No MCP servers could be started.\n\n")
		fmt.Printf("This usually happens when MCP server packages are not installed.\n")
		fmt.Printf("To install MCP servers via npx (they will be downloaded on first use):\n")
		fmt.Printf("  - The servers will be automatically downloaded when you run them\n")
		fmt.Printf("  - Make sure you have Node.js and npm installed\n\n")
		fmt.Printf("To check server status: agentgo mcp status --verbose\n")
		return fmt.Errorf("no MCP servers available")
	}

	// Call the tool
	fmt.Printf("🔍 Calling tool: %s\n", toolName)
	if len(toolArgs) > 0 {
		argsJSON, _ := json.MarshalIndent(toolArgs, "", "  ")
		fmt.Printf("📝 Arguments:\n%s\n\n", string(argsJSON))
	}

	callCtx, callCancel := context.WithTimeout(ctx, timeout)
	defer callCancel()

	result, err := toolManager.CallTool(callCtx, toolName, toolArgs)
	if err != nil {
		return fmt.Errorf("failed to call tool: %w", err)
	}

	if jsonOutput {
		output, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(output))
		return nil
	}

	// Human-readable output
	if result.Success {
		fmt.Printf("✅ Tool call succeeded (took %v)\n", result.Duration)
		fmt.Printf("📊 Result:\n")

		if result.Data != nil {
			if dataJSON, err := json.MarshalIndent(result.Data, "", "  "); err == nil {
				fmt.Println(string(dataJSON))
			} else {
				fmt.Printf("%v\n", result.Data)
			}
		}
	} else {
		fmt.Printf("❌ Tool call failed (took %v)\n", result.Duration)
		fmt.Printf("💥 Error: %s\n", result.Error)
	}

	return nil
}

func formatSchemaParams(props map[string]interface{}) string {
	var params []string
	for name, prop := range props {
		if propMap, ok := prop.(map[string]interface{}); ok {
			paramType := "any"
			if t, exists := propMap["type"]; exists {
				paramType = fmt.Sprintf("%v", t)
			}
			params = append(params, fmt.Sprintf("%s:%s", name, paramType))
		}
	}
	return strings.Join(params, ", ")
}
