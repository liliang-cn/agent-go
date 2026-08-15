package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPTool represents an MCP tool that can be called by LLM
type MCPTool interface {
	// Name returns the tool name
	Name() string
	// Description returns the tool description
	Description() string
	// ServerName returns the MCP server this tool belongs to
	ServerName() string
	// Schema returns the input schema for this tool
	Schema() map[string]interface{}
	// Annotations returns the tool's MCP behaviour hints, or nil when the
	// server sent none. See ToolAnnotations.
	Annotations() *ToolAnnotations
	// Call executes the tool with given arguments
	Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error)
}

// ToolAnnotations carries the behaviour hints an MCP server publishes with a
// tool: whether calling it changes anything, whether a change is destructive,
// whether repeating a call is safe.
//
// These exist so a host can decide what an agent is allowed to invoke without
// guessing from the tool's name — and guessing is genuinely unreliable, because
// names put the subject before the operation ("..._promoter_status" reads as a
// promotion to a substring match, and "widget" contains "get").
//
// Every field is a pointer: MCP treats an absent hint as unknown rather than
// false, and a host that conflates the two would silently downgrade an
// unannotated read-only tool into a mutating one, or worse the other way round.
type ToolAnnotations struct {
	// Title is a human-readable name for the tool.
	Title string
	// ReadOnlyHint is true when the tool does not modify its environment.
	ReadOnlyHint *bool
	// DestructiveHint is true when a change may be irreversible. Only
	// meaningful when the tool is not read-only.
	DestructiveHint *bool
	// IdempotentHint is true when repeating a call with the same arguments has
	// no additional effect.
	IdempotentHint *bool
	// OpenWorldHint is true when the tool touches something outside the
	// server's own closed world (the public internet, say).
	OpenWorldHint *bool
}

// IsReadOnly reports whether the server declared this tool read-only. The
// second result is false when no hint was published at all, which the caller
// must not read as "mutating" — it means the server said nothing.
func (a *ToolAnnotations) IsReadOnly() (readOnly, declared bool) {
	if a == nil || a.ReadOnlyHint == nil {
		return false, false
	}
	return *a.ReadOnlyHint, true
}

// MCPToolResult represents the result of an MCP tool call
type MCPToolResult struct {
	Success    bool          `json:"success"`
	Data       interface{}   `json:"data,omitempty"`
	Error      string        `json:"error,omitempty"`
	ServerName string        `json:"server_name"`
	ToolName   string        `json:"tool_name"`
	Duration   time.Duration `json:"duration"`
}

// MCPToolWrapper wraps an MCP tool for LLM usage
type MCPToolWrapper struct {
	client     *Client
	serverName string
	toolName   string
	tool       *mcp.Tool
	customTool MCPTool // For local/custom tools like memory
}

// NewMCPToolWrapper creates a new MCP tool wrapper
func NewMCPToolWrapper(client *Client, serverName string, tool *mcp.Tool) *MCPToolWrapper {
	return &MCPToolWrapper{
		client:     client,
		serverName: serverName,
		toolName:   tool.Name,
		tool:       tool,
	}
}

func (w *MCPToolWrapper) Name() string {
	if w.customTool != nil {
		return w.customTool.Name()
	}
	return fmt.Sprintf("mcp_%s_%s", w.serverName, w.toolName)
}

func (w *MCPToolWrapper) Description() string {
	if w.customTool != nil {
		return w.customTool.Description()
	}
	return fmt.Sprintf("[MCP:%s] %s", w.serverName, w.tool.Description)
}

func (w *MCPToolWrapper) ServerName() string {
	if w.customTool != nil {
		return w.customTool.ServerName()
	}
	return w.serverName
}

// Annotations implements MCPTool. It returns nil when the server published no
// hints for this tool, which is different from publishing "false".
func (w *MCPToolWrapper) Annotations() *ToolAnnotations {
	if w.customTool != nil {
		return w.customTool.Annotations()
	}
	if w.tool == nil || w.tool.Annotations == nil {
		return nil
	}
	a := w.tool.Annotations
	return &ToolAnnotations{
		Title: a.Title,
		// The SDK models readOnlyHint and idempotentHint as plain bools,
		// destructiveHint and openWorldHint as pointers. A plain false is
		// ambiguous — unset and "declared false" look identical over the
		// wire, since both omit the field — so it is reported as declared
		// only when true. A host loses nothing by this: either way it learns
		// the server did not promise read-only.
		ReadOnlyHint:    trueOrNil(a.ReadOnlyHint),
		DestructiveHint: a.DestructiveHint,
		IdempotentHint:  trueOrNil(a.IdempotentHint),
		OpenWorldHint:   a.OpenWorldHint,
	}
}

// trueOrNil renders a non-pointer hint as a pointer, collapsing false to "not
// declared" — see Annotations.
func trueOrNil(v bool) *bool {
	if !v {
		return nil
	}
	return &v
}

func (w *MCPToolWrapper) Schema() map[string]interface{} {
	if w.customTool != nil {
		return w.customTool.Schema()
	}
	// Try to convert the actual InputSchema to our format
	if w.tool.InputSchema != nil {
		// Use JSON marshaling/unmarshaling to convert any to map[string]interface{}
		schemaBytes, err := json.Marshal(w.tool.InputSchema)
		if err == nil {
			var schemaMap map[string]interface{}
			if err := json.Unmarshal(schemaBytes, &schemaMap); err == nil {
				// Successfully converted the schema
				return schemaMap
			}
		}
	}

	// Fallback: create a basic schema structure if conversion fails
	// This provides a generic object schema that allows any parameters
	schema := make(map[string]interface{})
	schema["type"] = "object"
	schema["properties"] = make(map[string]interface{})
	schema["additionalProperties"] = true // Allow any additional properties

	return schema
}

func (w *MCPToolWrapper) Call(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	start := time.Now()

	if w.customTool != nil {
		return w.customTool.Call(ctx, args)
	}

	result := &MCPToolResult{
		ServerName: w.serverName,
		ToolName:   w.toolName,
		Duration:   0,
	}

	// Call the underlying MCP tool
	toolResult, err := w.client.CallTool(ctx, w.toolName, args)
	result.Duration = time.Since(start)

	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("MCP tool call failed: %v", err)
		return result, nil // Don't return error, return failed result
	}

	result.Success = toolResult.Success
	result.Data = toolResult.Data
	if !toolResult.Success {
		result.Error = toolResult.Error
	}

	return result, nil
}

// MCPToolManager manages MCP tools for LLM usage
type MCPToolManager struct {
	manager *Manager
	tools   map[string]*MCPToolWrapper
	mu      sync.RWMutex // Protects tools map
}

// NewMCPToolManager creates a new MCP tool manager
func NewMCPToolManager(mcpConfig *Config) *MCPToolManager {
	return &MCPToolManager{
		manager: NewManager(mcpConfig),
		tools:   make(map[string]*MCPToolWrapper),
	}
}

// Start initializes MCP servers and loads tools
func (tm *MCPToolManager) Start(ctx context.Context) error {
	if !tm.manager.config.Enabled {
		return fmt.Errorf("MCP is disabled in configuration")
	}

	// Load server configurations from JSON files
	if err := tm.manager.config.LoadServersFromJSON(); err != nil {
		return fmt.Errorf("failed to load server configurations: %w", err)
	}

	// Start auto-start servers
	loadedServers := tm.manager.config.GetLoadedServers()
	fmt.Printf("[MCP] Found %d configured servers\n", len(loadedServers))
	for _, serverConfig := range loadedServers {
		fmt.Printf("[MCP] Server '%s': AutoStart=%v, Command=%v, Args=%v\n",
			serverConfig.Name, serverConfig.AutoStart, serverConfig.Command, serverConfig.Args)
		if serverConfig.AutoStart {
			fmt.Printf("[MCP] Starting server '%s'...\n", serverConfig.Name)
			if err := tm.StartServer(ctx, serverConfig.Name); err != nil {
				fmt.Printf("[MCP] Failed to start server '%s': %v\n", serverConfig.Name, err)
				return fmt.Errorf("failed to start server %s: %w", serverConfig.Name, err)
			}
			fmt.Printf("[MCP] Server '%s' started successfully\n", serverConfig.Name)
		}
	}

	return nil
}

// StartServer starts a specific MCP server and loads its tools
func (tm *MCPToolManager) StartServer(ctx context.Context, serverName string) error {
	client, err := tm.manager.StartServer(ctx, serverName)
	if err != nil {
		return fmt.Errorf("failed to start MCP server %s: %w", serverName, err)
	}

	// Load tools from the server
	tools := client.GetTools()
	tm.mu.Lock()
	for _, tool := range tools {
		wrapper := NewMCPToolWrapper(client, serverName, tool)
		tm.tools[wrapper.Name()] = wrapper
	}
	tm.mu.Unlock()

	return nil
}

// StartWithFailures initializes MCP servers, continuing even if some fail
func (tm *MCPToolManager) StartWithFailures(ctx context.Context) ([]string, []string) {
	succeeded, failed, _ := tm.StartWithFailuresDetailed(ctx)
	return succeeded, failed
}

// StartWithFailuresDetailed initializes MCP servers with detailed error information
func (tm *MCPToolManager) StartWithFailuresDetailed(ctx context.Context) ([]string, []string, map[string]error) {
	var succeeded []string
	var failed []string
	errors := make(map[string]error)

	if !tm.manager.config.Enabled {
		return succeeded, failed, errors
	}

	// Load server configurations from JSON files
	if err := tm.manager.config.LoadServersFromJSON(); err != nil {
		// If we can't load configs, all servers are considered failed
		return succeeded, failed, errors
	}

	// Start auto-start servers
	loadedServers := tm.manager.config.GetLoadedServers()
	for _, serverConfig := range loadedServers {
		if serverConfig.AutoStart {
			if err := tm.StartServer(ctx, serverConfig.Name); err != nil {
				failed = append(failed, serverConfig.Name)
				errors[serverConfig.Name] = err
			} else {
				succeeded = append(succeeded, serverConfig.Name)
			}
		}
	}

	return succeeded, failed, errors
}

// StopServer stops a specific MCP server and removes its tools
func (tm *MCPToolManager) StopServer(serverName string) error {
	// Remove tools from this server
	for name, tool := range tm.tools {
		if tool.ServerName() == serverName {
			delete(tm.tools, name)
		}
	}

	return tm.manager.StopServer(serverName)
}

// GetTool returns a specific MCP tool by name
func (tm *MCPToolManager) GetTool(name string) (*MCPToolWrapper, bool) {
	tm.mu.RLock()
	tool, exists := tm.tools[name]
	tm.mu.RUnlock()
	return tool, exists
}

// ListTools returns all available MCP tools
func (tm *MCPToolManager) ListTools() map[string]*MCPToolWrapper {
	tm.mu.RLock()
	tools := make(map[string]*MCPToolWrapper)
	for name, tool := range tm.tools {
		tools[name] = tool
	}
	tm.mu.RUnlock()
	return tools
}

// ListToolsByServer returns tools from a specific server
func (tm *MCPToolManager) ListToolsByServer(serverName string) map[string]*MCPToolWrapper {
	tm.mu.RLock()
	tools := make(map[string]*MCPToolWrapper)
	for name, tool := range tm.tools {
		if tool.ServerName() == serverName {
			tools[name] = tool
		}
	}
	tm.mu.RUnlock()
	return tools
}

// CallTool calls an MCP tool by name
func (tm *MCPToolManager) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (*MCPToolResult, error) {
	tm.mu.RLock()
	tool, exists := tm.tools[toolName]
	tm.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("MCP tool '%s' not found", toolName)
	}

	return tool.Call(ctx, args)
}

// GetToolsForLLM returns tools in a format suitable for LLM function calling
func (tm *MCPToolManager) GetToolsForLLM() []map[string]interface{} {
	llmTools := make([]map[string]interface{}, 0)

	tm.mu.RLock()
	for _, tool := range tm.tools {
		llmTool := map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool.Name(),
				"description": tool.Description(),
				"parameters":  tool.Schema(),
			},
		}
		llmTools = append(llmTools, llmTool)
	}
	tm.mu.RUnlock()

	return llmTools
}

// GetServerStatus returns status of all MCP servers
func (tm *MCPToolManager) GetServerStatus() map[string]bool {
	status := make(map[string]bool)
	clients := tm.manager.ListClients()

	for name, client := range clients {
		status[name] = client.IsConnected()
	}

	// Also check configured servers that might not be running
	loadedServers := tm.manager.config.GetLoadedServers()
	for _, serverConfig := range loadedServers {
		if _, exists := status[serverConfig.Name]; !exists {
			status[serverConfig.Name] = false
		}
	}

	return status
}

// Close stops all MCP servers and cleans up
func (tm *MCPToolManager) Close() error {
	tm.mu.Lock()
	tm.tools = make(map[string]*MCPToolWrapper)
	tm.mu.Unlock()
	return tm.manager.Close()
}

// ToolUsageStats represents usage statistics for MCP tools
type ToolUsageStats struct {
	ToolName      string        `json:"tool_name"`
	ServerName    string        `json:"server_name"`
	CallCount     int64         `json:"call_count"`
	SuccessCount  int64         `json:"success_count"`
	ErrorCount    int64         `json:"error_count"`
	TotalDuration time.Duration `json:"total_duration"`
	AvgDuration   time.Duration `json:"avg_duration"`
	LastUsed      time.Time     `json:"last_used"`
}

// GetUsageStats returns usage statistics for all tools (placeholder for future implementation)
func (tm *MCPToolManager) GetUsageStats() map[string]*ToolUsageStats {
	// This would be implemented with actual usage tracking
	// For now, return empty stats
	return make(map[string]*ToolUsageStats)
}
