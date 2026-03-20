package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Service provides high-level MCP operations
type Service struct {
	manager       *Manager
	llm           domain.Generator
	mcpConfig     *Config
	conversations map[string]*Conversation
	convMu        sync.RWMutex
}

// Conversation represents a chat conversation with history
type Conversation struct {
	ID       string
	Messages []domain.Message
}

// NewService creates a new MCP service
func NewService(mcpConfig *Config, llm domain.Generator) (*Service, error) {
	// Load MCP servers from JSON
	if err := mcpConfig.LoadServersFromJSON(); err != nil {
		return nil, fmt.Errorf("failed to load MCP servers: %w", err)
	}

	// Ensure built-in servers (filesystem, websearch) are always present.
	// They are in-process — no external binary needed.
	// FilesystemDirs from config overrides the default (user home dir).
	loadedServers := mcpConfig.GetLoadedServers()
	for _, builtin := range GetBuiltInServers(mcpConfig.FilesystemDirs) {
		found := false
		for _, loaded := range loadedServers {
			if loaded.Name == builtin.Name {
				found = true
				break
			}
		}
		if !found {
			mcpConfig.AddServer(&builtin)
		}
	}

	// Create MCP manager
	manager := NewManager(mcpConfig)

	return &Service{
		manager:       manager,
		llm:           llm,
		mcpConfig:     mcpConfig,
		conversations: make(map[string]*Conversation),
	}, nil
}

// StartServers starts MCP servers based on configuration
func (s *Service) StartServers(ctx context.Context, serverNames []string) error {
	if len(serverNames) == 0 {
		// Start all configured servers
		loadedServers := s.mcpConfig.GetLoadedServers()
		for _, server := range loadedServers {
			if server.AutoStart {
				serverNames = append(serverNames, server.Name)
			}
		}
	}

	for _, name := range serverNames {
		if _, err := s.manager.StartServer(ctx, name); err != nil {
			return fmt.Errorf("failed to start server %s: %w", name, err)
		}
	}

	return nil
}

// StopServer stops a specific MCP server
func (s *Service) StopServer(serverName string) error {
	return s.manager.StopServer(serverName)
}

// AddDynamicServer adds and starts a server dynamically
func (s *Service) AddDynamicServer(ctx context.Context, name string, command string, args []string) error {
	// Create server configuration
	serverConfig := &ServerConfig{
		Name:      name,
		Type:      ServerTypeStdio,
		Command:   []string{command},
		Args:      args,
		AutoStart: true,
	}

	// Register with config
	s.mcpConfig.AddServer(serverConfig)

	// Start it via manager
	_, err := s.manager.StartServer(ctx, name)
	return err
}

// ListServers returns a list of configured servers and their status
func (s *Service) ListServers() []ServerStatus {
	var servers []ServerStatus

	loadedServers := s.mcpConfig.GetLoadedServers()
	for _, config := range loadedServers {
		client, exists := s.manager.GetClient(config.Name)

		status := ServerStatus{
			Name:        config.Name,
			Description: config.Description,
			Command:     strings.Join(config.Command, " "),
			Running:     exists && client != nil && client.IsConnected(),
		}

		if status.Running {
			tools := client.GetTools()
			status.ToolCount = len(tools)
			for _, tool := range tools {
				status.Tools = append(status.Tools, ToolSummary{
					Name:        tool.Name,
					Description: tool.Description,
					ServerName:  config.Name,
				})
			}
		}

		servers = append(servers, status)
	}

	return servers
}

// ServerStatus represents the status of an MCP server
type ServerStatus struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Command     string        `json:"command"`
	Running     bool          `json:"running"`
	ToolCount   int           `json:"tool_count"`
	Tools       []ToolSummary `json:"tools,omitempty"`
}

// GetAvailableTools returns all available tools from all running servers
func (s *Service) GetAvailableTools(ctx context.Context) []AgentToolInfo {
	return s.manager.GetAvailableTools(ctx)
}

// GetServerCount returns the count of connected MCP servers
func (s *Service) GetServerCount() int {
	return s.manager.GetServerCount()
}

// CallTool calls a specific tool by name
func (s *Service) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*ToolResult, error) {
	if err := validateFilesystemToolArgs(toolName, arguments, s.mcpConfig.FilesystemIgnore); err != nil {
		return nil, err
	}
	arguments = sanitizeFilesystemToolArgs(toolName, arguments)
	result, err := s.manager.CallTool(ctx, toolName, arguments)
	if err != nil {
		return nil, err
	}
	return filterFilesystemToolResult(toolName, result, s.mcpConfig.FilesystemIgnore), nil
}

// ChatOptions configures MCP-enabled chat
type ChatOptions struct {
	Model          string   // LLM model to use
	Temperature    float64  // Generation temperature
	MaxTokens      int      // Maximum tokens
	SystemPrompt   string   // System prompt
	AllowedServers []string // Specific servers to use
	MaxToolCalls   int      // Maximum tool calls per message
	ShowToolCalls  bool     // Display tool calls in response
}

// DefaultChatOptions returns default chat options
func DefaultChatOptions() *ChatOptions {
	return &ChatOptions{
		Temperature:   0.7,
		MaxTokens:     2000,
		MaxToolCalls:  10,
		ShowToolCalls: true,
	}
}

// ChatSingle performs a single MCP chat interaction (no memory)
func (s *Service) ChatSingle(ctx context.Context, message string, opts *ChatOptions) (*ChatResponse, error) {
	if opts == nil {
		opts = DefaultChatOptions()
	}

	// Get available tools
	tools := s.manager.GetAvailableTools(ctx)

	// Filter tools by allowed servers if specified
	if len(opts.AllowedServers) > 0 {
		filtered := []AgentToolInfo{}
		for _, tool := range tools {
			for _, allowed := range opts.AllowedServers {
				if tool.ServerName == allowed {
					filtered = append(filtered, tool)
					break
				}
			}
		}
		tools = filtered
	}

	// Convert to tool definitions for LLM
	toolDefs := make([]domain.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		// Use the full InputSchema if available
		var parameters map[string]interface{}
		if tool.InputSchema != nil && len(tool.InputSchema) > 0 {
			parameters = tool.InputSchema
		} else {
			parameters = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}

		toolDefs = append(toolDefs, domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}

	// Create messages
	messages := []domain.Message{
		{
			Role:    "user",
			Content: message,
		},
	}

	// Add system prompt if provided
	if opts.SystemPrompt != "" {
		messages = append([]domain.Message{
			{
				Role:    "system",
				Content: opts.SystemPrompt,
			},
		}, messages...)
	}

	// Generate with tools
	genOpts := &domain.GenerationOptions{
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
	}

	result, err := s.llm.GenerateWithTools(ctx, messages, toolDefs, genOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate response: %w", err)
	}

	// Execute tool calls if any
	var executedCalls []ExecutedToolCall
	if len(result.ToolCalls) > 0 && opts.MaxToolCalls > 0 {
		for i, call := range result.ToolCalls {
			if i >= opts.MaxToolCalls {
				break
			}

			// Execute the tool
			toolResult, err := s.manager.CallTool(ctx, call.Function.Name, call.Function.Arguments)

			executed := ExecutedToolCall{
				ToolName:  call.Function.Name,
				Arguments: call.Function.Arguments,
				Success:   toolResult != nil && toolResult.Success,
			}

			if err != nil {
				executed.Error = err.Error()
			} else if toolResult != nil {
				executed.Result = toolResult.Data
			}

			executedCalls = append(executedCalls, executed)
		}
	}

	return &ChatResponse{
		Message:   result.Content,
		ToolCalls: executedCalls,
	}, nil
}

// ============================================
// Chat API with Memory
// ============================================

// Chat sends a message with conversation history (auto UUID session)
func (s *Service) Chat(ctx context.Context, message string) (*ChatResponse, error) {
	s.convMu.Lock()
	if len(s.conversations) == 0 {
		conv := &Conversation{
			ID:       uuid.New().String(),
			Messages: []domain.Message{},
		}
		s.conversations[conv.ID] = conv
	}
	var convID string
	for id := range s.conversations {
		convID = id
		break
	}
	s.convMu.Unlock()

	return s.ChatWithID(ctx, convID, message, nil)
}

// ChatWithID sends a message to a specific conversation
func (s *Service) ChatWithID(ctx context.Context, convID, message string, opts *ChatOptions) (*ChatResponse, error) {
	s.convMu.Lock()
	conv, exists := s.conversations[convID]
	if !exists {
		conv = &Conversation{
			ID:       convID,
			Messages: []domain.Message{},
		}
		s.conversations[convID] = conv
	}
	s.convMu.Unlock()

	if opts == nil {
		opts = DefaultChatOptions()
	}

	// Add user message
	conv.Messages = append(conv.Messages, domain.Message{
		Role:    "user",
		Content: message,
	})

	// Get tools
	tools := s.manager.GetAvailableTools(ctx)
	if len(opts.AllowedServers) > 0 {
		filtered := []AgentToolInfo{}
		for _, tool := range tools {
			for _, allowed := range opts.AllowedServers {
				if tool.ServerName == allowed {
					filtered = append(filtered, tool)
					break
				}
			}
		}
		tools = filtered
	}

	// Convert to tool definitions
	toolDefs := make([]domain.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		var parameters map[string]interface{}
		if tool.InputSchema != nil && len(tool.InputSchema) > 0 {
			parameters = tool.InputSchema
		} else {
			parameters = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		toolDefs = append(toolDefs, domain.ToolDefinition{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}

	// Build messages with history
	messages := make([]domain.Message, len(conv.Messages))
	copy(messages, conv.Messages)

	// Add system prompt if provided
	if opts.SystemPrompt != "" {
		messages = append([]domain.Message{
			{Role: "system", Content: opts.SystemPrompt},
		}, messages...)
	}

	// Generate
	genOpts := &domain.GenerationOptions{
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
	}

	result, err := s.llm.GenerateWithTools(ctx, messages, toolDefs, genOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate: %w", err)
	}

	// Execute tool calls
	var executedCalls []ExecutedToolCall
	if len(result.ToolCalls) > 0 && opts.MaxToolCalls > 0 {
		for i, call := range result.ToolCalls {
			if i >= opts.MaxToolCalls {
				break
			}
			toolResult, err := s.manager.CallTool(ctx, call.Function.Name, call.Function.Arguments)
			executed := ExecutedToolCall{
				ToolName:  call.Function.Name,
				Arguments: call.Function.Arguments,
				Success:   toolResult != nil && toolResult.Success,
			}
			if err != nil {
				executed.Error = err.Error()
			} else if toolResult != nil {
				executed.Result = toolResult.Data
			}
			executedCalls = append(executedCalls, executed)
		}
	}

	// Add assistant response to conversation
	conv.Messages = append(conv.Messages, domain.Message{
		Role:    "assistant",
		Content: result.Content,
	})

	return &ChatResponse{
		Message:   result.Content,
		ToolCalls: executedCalls,
	}, nil
}

// CurrentConversationID returns the current conversation ID
func (s *Service) CurrentConversationID() string {
	s.convMu.RLock()
	defer s.convMu.RUnlock()
	for id := range s.conversations {
		return id
	}
	return ""
}

// ResetConversation clears current conversation and starts a new one
func (s *Service) ResetConversation() string {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	s.conversations = make(map[string]*Conversation)
	convID := uuid.New().String()
	s.conversations[convID] = &Conversation{
		ID:       convID,
		Messages: []domain.Message{},
	}
	return convID
}

// GetConversationMessages returns all messages in a conversation
func (s *Service) GetConversationMessages(convID string) []domain.Message {
	s.convMu.RLock()
	defer s.convMu.RUnlock()
	if conv, exists := s.conversations[convID]; exists {
		messages := make([]domain.Message, len(conv.Messages))
		copy(messages, conv.Messages)
		return messages
	}
	return nil
}

// ChatResponse represents a chat response with potential tool calls
type ChatResponse struct {
	Message   string             `json:"message"`
	ToolCalls []ExecutedToolCall `json:"tool_calls,omitempty"`
}

// ExecutedToolCall represents a tool call that was executed
type ExecutedToolCall struct {
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
	Result    interface{}            `json:"result,omitempty"`
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
}

// TestTool tests a specific tool with sample arguments
func (s *Service) TestTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*ToolResult, error) {
	// Find the tool
	toolInfo, _, err := s.manager.FindToolProvider(ctx, toolName)
	if err != nil {
		return nil, fmt.Errorf("tool not found: %w", err)
	}

	// Call the tool
	result, err := s.manager.CallTool(ctx, toolName, arguments)
	if err != nil {
		return nil, fmt.Errorf("tool execution failed: %w", err)
	}

	// Add tool info to result for context
	if result != nil {
		if metadata, ok := result.Data.(map[string]interface{}); ok {
			metadata["_tool_info"] = map[string]string{
				"name":   toolInfo.Name,
				"server": toolInfo.ServerName,
			}
		}
	}

	return result, nil
}

// Close closes the MCP service and all connections
func (s *Service) Close() error {
	if s.manager != nil {
		return s.manager.Close()
	}
	return nil
}

// GetManager returns the underlying manager for advanced operations
func (s *Service) GetManager() *Manager {
	return s.manager
}
