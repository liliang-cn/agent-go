package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
)

// MCPTaskOutput represents the output of an MCP task execution
type MCPTaskOutput struct {
	Tool      string                 `json:"tool"`
	Arguments map[string]interface{} `json:"arguments"`
	Result    string                 `json:"result"`
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
}

// IntelligentMCPOutput represents the output of intelligent MCP mode execution
type IntelligentMCPOutput struct {
	Response  string   `json:"response"`
	ToolsUsed []string `json:"tools_used"`
	Success   bool     `json:"success"`
	Error     string   `json:"error,omitempty"`
}

// MCPExecutor executes MCP tool call tasks
type MCPExecutor struct {
	config         *config.Config
	mcpClient      *mcp.Client
	mcpToolManager *mcp.MCPToolManager
	llmService     domain.LLMService
}

// NewMCPExecutor creates a new MCP executor
func NewMCPExecutor(cfg *config.Config) *MCPExecutor {
	return &MCPExecutor{
		config: cfg,
	}
}

// SetMCPClient sets the MCP client for this executor
func (e *MCPExecutor) SetMCPClient(client *mcp.Client) {
	e.mcpClient = client
}

// SetMCPToolManager sets the MCP tool manager for this executor
func (e *MCPExecutor) SetMCPToolManager(manager *mcp.MCPToolManager) {
	e.mcpToolManager = manager
}

// SetLLMService sets the LLM service for this executor
func (e *MCPExecutor) SetLLMService(service domain.LLMService) {
	e.llmService = service
}

// Type returns the task type this executor handles
func (e *MCPExecutor) Type() scheduler.TaskType {
	return scheduler.TaskTypeMCP
}

// Validate checks if the parameters are valid for MCP execution
func (e *MCPExecutor) Validate(parameters map[string]string) error {
	// tool parameter is now optional - if not specified, will use built-in tools
	return nil
}

// Execute runs an MCP tool call task
func (e *MCPExecutor) Execute(ctx context.Context, parameters map[string]string) (*scheduler.TaskResult, error) {
	toolName := parameters["tool"]

	// Check if we should use intelligent MCP mode (no tool specified)
	if toolName == "" {
		return e.executeIntelligentMCP(ctx, parameters)
	}

	// Direct tool call mode
	return e.executeDirectToolCall(ctx, toolName, parameters)
}

// executeIntelligentMCP uses LLM + MCP tools to handle the request intelligently
func (e *MCPExecutor) executeIntelligentMCP(ctx context.Context, parameters map[string]string) (*scheduler.TaskResult, error) {
	// Get the user message/query
	message := parameters["message"]
	if message == "" {
		message = parameters["query"] // fallback to query parameter
	}
	if message == "" {
		output := MCPTaskOutput{
			Tool:      "intelligent_mcp",
			Arguments: map[string]interface{}{"parameters": parameters},
			Result:    "No message or query provided for intelligent MCP mode",
			Success:   false,
		}
		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true,
			Output:  string(outputJSON),
		}, nil
	}

	// Check if services are available
	if e.llmService == nil {
		output := MCPTaskOutput{
			Tool:      "intelligent_mcp",
			Arguments: map[string]interface{}{"message": message},
			Result:    "LLM service is not available for intelligent MCP mode",
			Success:   false,
		}
		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true,
			Output:  string(outputJSON),
		}, nil
	}

	// Use configured MCP tool manager, or create one if not available
	mcpManager := e.mcpToolManager
	if mcpManager == nil {
		// Fallback: create and start MCP tool manager
		mcpManager = mcp.NewMCPToolManager(&e.config.MCP)
		ctx := context.Background()
		if err := mcpManager.Start(ctx); err != nil {
			output := MCPTaskOutput{
				Tool:      "intelligent_mcp",
				Arguments: map[string]interface{}{"message": message},
				Result:    fmt.Sprintf("Failed to start MCP services: %v", err),
				Success:   false,
			}
			outputJSON, _ := json.MarshalIndent(output, "", "  ")
			return &scheduler.TaskResult{
				Success: true,
				Output:  string(outputJSON),
			}, nil
		}
	}

	// Get available tools
	toolsMap := mcpManager.ListTools()
	if len(toolsMap) == 0 {
		output := MCPTaskOutput{
			Tool:      "intelligent_mcp",
			Arguments: map[string]interface{}{"message": message},
			Result:    "No MCP tools available",
			Success:   false,
		}
		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true,
			Output:  string(outputJSON),
		}, nil
	}

	// Convert to slice and build tool definitions
	var tools []*mcp.MCPToolWrapper
	for _, tool := range toolsMap {
		tools = append(tools, tool)
	}

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
	showThinking := true
	opts := &domain.GenerationOptions{
		Temperature: 0.7,
		MaxTokens:   30000,
		Think:       &showThinking,
	}

	// Call LLM with tools
	result, err := e.llmService.GenerateWithTools(ctx, messages, toolDefinitions, opts)
	if err != nil {
		output := MCPTaskOutput{
			Tool:      "intelligent_mcp",
			Arguments: map[string]interface{}{"message": message},
			Result:    fmt.Sprintf("LLM generation failed: %v", err),
			Success:   false,
		}
		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true,
			Output:  string(outputJSON),
		}, nil
	}

	// Handle tool calls if any
	var toolsUsed []string
	if len(result.ToolCalls) > 0 {
		for _, toolCall := range result.ToolCalls {
			// Execute tool call via MCP
			mcpResult, err := mcpManager.CallTool(ctx, toolCall.Function.Name, toolCall.Function.Arguments)

			// Add tool name to list
			toolsUsed = append(toolsUsed, toolCall.Function.Name)

			// Log tool call result for debugging
			if err != nil {
				log.Printf("Tool call %s failed: %v", toolCall.Function.Name, err)
			} else if mcpResult.Error != "" {
				log.Printf("Tool call %s returned error: %s", toolCall.Function.Name, mcpResult.Error)
			}
		}
	}

	// Create comprehensive output
	output := IntelligentMCPOutput{
		Response:  result.Content,
		ToolsUsed: toolsUsed,
		Success:   true,
	}

	outputJSON, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return &scheduler.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to marshal output: %v", err),
		}, nil
	}

	return &scheduler.TaskResult{
		Success: true,
		Output:  string(outputJSON),
	}, nil
}

// executeDirectToolCall executes a specific MCP tool
func (e *MCPExecutor) executeDirectToolCall(ctx context.Context, toolName string, parameters map[string]string) (*scheduler.TaskResult, error) {
	// Extract arguments (default to empty if not provided)
	argsStr := parameters["args"]
	if argsStr == "" {
		argsStr = "{}"
	}

	// Try to parse arguments as JSON
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		args = make(map[string]interface{})
	}

	// Use configured MCP tool manager, or create one if not available
	mcpManager := e.mcpToolManager
	if mcpManager == nil {
		// Fallback: create and start MCP tool manager
		mcpManager = mcp.NewMCPToolManager(&e.config.MCP)
		if err := mcpManager.Start(ctx); err != nil {
			output := MCPTaskOutput{
				Tool:      toolName,
				Arguments: args,
				Result:    fmt.Sprintf("Failed to start MCP services: %v", err),
				Success:   false,
			}
			outputJSON, _ := json.MarshalIndent(output, "", "  ")
			return &scheduler.TaskResult{
				Success: true,
				Output:  string(outputJSON),
			}, nil
		}
	}

	// Check if tool exists
	tools := mcpManager.ListTools()
	if _, exists := tools[toolName]; !exists {
		output := MCPTaskOutput{
			Tool:      toolName,
			Arguments: args,
			Result:    fmt.Sprintf("MCP tool '%s' not found. Available tools: %v", toolName, getToolNames(tools)),
			Success:   false,
		}
		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true,
			Output:  string(outputJSON),
		}, nil
	}

	// Attempt to call the MCP tool
	result, err := mcpManager.CallTool(ctx, toolName, args)
	if err != nil {
		output := MCPTaskOutput{
			Tool:      toolName,
			Arguments: args,
			Result:    fmt.Sprintf("Failed to call MCP tool '%s': %v", toolName, err),
			Success:   false,
		}

		outputJSON, _ := json.MarshalIndent(output, "", "  ")
		return &scheduler.TaskResult{
			Success: true, // Task execution succeeded, even if tool call failed
			Output:  string(outputJSON),
		}, nil
	}

	// Create output structure for successful call
	output := MCPTaskOutput{
		Tool:      toolName,
		Arguments: args,
		Result:    fmt.Sprintf("MCP tool call successful: %v", result),
		Success:   true,
	}

	outputJSON, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return &scheduler.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to marshal output: %v", err),
		}, nil
	}

	return &scheduler.TaskResult{
		Success: true,
		Output:  string(outputJSON),
	}, nil
}

// getToolNames extracts tool names from tools map
func getToolNames(tools map[string]*mcp.MCPToolWrapper) []string {
	var names []string
	for name := range tools {
		names = append(names, name)
	}
	return names
}
