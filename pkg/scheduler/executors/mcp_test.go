package executors

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
	"github.com/liliang-cn/agent-go/v2/pkg/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMCPExecutor(t *testing.T) {
	cfg := &config.Config{}
	executor := NewMCPExecutor(cfg)
	require.NotNil(t, executor)
	assert.NotNil(t, executor.config)
	assert.Nil(t, executor.mcpClient)
	assert.Nil(t, executor.mcpToolManager)
	assert.Nil(t, executor.llmService)
}

func TestMCPExecutorType(t *testing.T) {
	executor := NewMCPExecutor(nil)
	assert.Equal(t, scheduler.TaskTypeMCP, executor.Type())
}

func TestMCPExecutorValidate(t *testing.T) {
	executor := NewMCPExecutor(nil)

	tests := []struct {
		name       string
		parameters map[string]string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "Valid with tool and prompt",
			parameters: map[string]string{"tool": "filesystem", "prompt": "List files"},
			wantErr:    false,
		},
		{
			name:       "Valid with only prompt",
			parameters: map[string]string{"prompt": "Do something intelligent"},
			wantErr:    false,
		},
		{
			name:       "Valid with tool and action",
			parameters: map[string]string{"tool": "filesystem", "action": "read_file"},
			wantErr:    false,
		},
		{
			name:       "Empty parameters (valid - will use defaults)",
			parameters: map[string]string{},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := executor.Validate(tt.parameters)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMCPExecutorExecuteWithoutServices(t *testing.T) {
	cfg := &config.Config{
		MCP: mcp.Config{},
	}
	executor := NewMCPExecutor(cfg)
	ctx := context.Background()

	tests := []struct {
		name       string
		parameters map[string]string
	}{
		{
			name:       "Tool-based execution",
			parameters: map[string]string{"tool": "filesystem", "action": "list"},
		},
		{
			name:       "Prompt-based execution",
			parameters: map[string]string{"prompt": "List all files"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.Success)

			// Parse output
			var output MCPTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)
			// Success is false without MCP client
			assert.False(t, output.Success)
			if output.Error != "" {
				assert.Contains(t, output.Error, "MCP")
			} else {
				assert.Contains(t, output.Result, "MCP")
			}
		})
	}
}

func TestMCPExecutorSetters(t *testing.T) {
	executor := NewMCPExecutor(nil)

	// Test SetMCPClient
	assert.Nil(t, executor.mcpClient)
	executor.SetMCPClient(nil)
	assert.Nil(t, executor.mcpClient)

	// Test SetMCPToolManager
	assert.Nil(t, executor.mcpToolManager)
	executor.SetMCPToolManager(nil)
	assert.Nil(t, executor.mcpToolManager)

	// Test SetLLMService
	assert.Nil(t, executor.llmService)
	executor.SetLLMService(nil)
	assert.Nil(t, executor.llmService)
}

func TestMCPExecutorIntelligentMode(t *testing.T) {
	cfg := &config.Config{
		MCP: mcp.Config{},
	}
	executor := NewMCPExecutor(cfg)
	ctx := context.Background()

	// Test intelligent mode with message
	params := map[string]string{
		"message": "Find all Python files",
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
}

func TestMCPExecutorDirectMode(t *testing.T) {
	cfg := &config.Config{
		MCP: mcp.Config{},
	}
	executor := NewMCPExecutor(cfg)
	ctx := context.Background()

	// Test direct tool call with arguments
	params := map[string]string{
		"tool": "filesystem",
		"args": `{"action": "list", "path": "/test"}`,
	}

	result, err := executor.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	var output MCPTaskOutput
	err = json.Unmarshal([]byte(result.Output), &output)
	require.NoError(t, err)
	assert.Equal(t, "filesystem", output.Tool)
}

func TestGetToolNames(t *testing.T) {
	// Test nil map
	names := getToolNames(nil)
	assert.Empty(t, names)

	// Test empty map
	names = getToolNames(map[string]*mcp.MCPToolWrapper{})
	assert.Empty(t, names)

	// Test with tools
	tools := map[string]*mcp.MCPToolWrapper{
		"tool1": {},
		"tool2": {},
		"tool3": {},
	}
	names = getToolNames(tools)
	assert.Len(t, names, 3)
	assert.Contains(t, names, "tool1")
	assert.Contains(t, names, "tool2")
	assert.Contains(t, names, "tool3")
}

func TestMCPExecutorParameterExtraction(t *testing.T) {
	cfg := &config.Config{
		MCP: mcp.Config{},
	}
	executor := NewMCPExecutor(cfg)
	ctx := context.Background()

	tests := []struct {
		name               string
		parameters         map[string]string
		expectedToolCall   bool
		expectedPromptCall bool
	}{
		{
			name: "Tool with arguments",
			parameters: map[string]string{
				"tool":      "filesystem",
				"action":    "read_file",
				"arguments": `{"path": "/test.txt"}`,
			},
			expectedToolCall:   true,
			expectedPromptCall: false,
		},
		{
			name: "Prompt only",
			parameters: map[string]string{
				"prompt": "Find all Python files",
			},
			expectedToolCall:   false,
			expectedPromptCall: true,
		},
		{
			name: "Tool without action (intelligent mode)",
			parameters: map[string]string{
				"tool":   "filesystem",
				"prompt": "List all files",
			},
			expectedToolCall:   false,
			expectedPromptCall: true,
		},
		{
			name: "Tool with JSON args",
			parameters: map[string]string{
				"tool": "filesystem",
				"args": `{"action": "list_directory", "path": "/"}`,
			},
			expectedToolCall:   true,
			expectedPromptCall: false,
		},
		{
			name: "Message parameter (intelligent mode)",
			parameters: map[string]string{
				"message": "Search for configuration files",
			},
			expectedToolCall:   false,
			expectedPromptCall: true,
		},
		{
			name:               "Empty parameters (uses defaults)",
			parameters:         map[string]string{},
			expectedToolCall:   false,
			expectedPromptCall: true,
		},
		{
			name: "Tool and action without arguments",
			parameters: map[string]string{
				"tool":   "filesystem",
				"action": "list",
			},
			expectedToolCall:   true,
			expectedPromptCall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(ctx, tt.parameters)
			require.NoError(t, err)
			require.NotNil(t, result)

			var output MCPTaskOutput
			err = json.Unmarshal([]byte(result.Output), &output)
			require.NoError(t, err)
			assert.False(t, output.Success)
		})
	}
}
