// Package ptc provides Programmatic Tool Calling functionality,
// allowing LLMs to generate code instead of JSON parameters for tool calls.
// The code is executed safely in a JavaScript sandbox.
package ptc

import (
	"context"
	"time"
)

// RuntimeType defines the sandbox runtime type
type RuntimeType string

const (
	// RuntimeGoja uses the Goja JavaScript interpreter (pure Go).
	RuntimeGoja RuntimeType = "goja"
)

// LanguageType defines the supported code languages
type LanguageType string

const (
	LanguageJavaScript LanguageType = "javascript"
)

// ExecutionRequest represents a PTC execution request
type ExecutionRequest struct {
	// ID is the unique identifier for this execution
	ID string `json:"id"`
	// Code is the LLM-generated code to execute
	Code string `json:"code"`
	// Language specifies the code language (javascript)
	Language LanguageType `json:"language"`
	// Context contains variables to inject into the sandbox
	Context map[string]interface{} `json:"context"`
	// Tools is the list of allowed tool names
	Tools []string `json:"tools"`
	// Timeout is the maximum execution time
	Timeout time.Duration `json:"timeout"`
	// MaxMemoryMB is the maximum memory in megabytes
	MaxMemoryMB int `json:"max_memory_mb"`
}

// ExecutionResult represents the result of PTC execution
type ExecutionResult struct {
	// ID is the execution ID
	ID string `json:"id"`
	// Success indicates whether execution succeeded
	Success bool `json:"success"`
	// Output is the stdout/output from execution
	Output interface{} `json:"output"`
	// ReturnValue is the value returned by the code
	ReturnValue interface{} `json:"return_value,omitempty"`
	// ToolCalls records all tool calls made during execution
	ToolCalls []ToolCallRecord `json:"tool_calls,omitempty"`
	// Logs contains console.log output
	Logs []string `json:"logs,omitempty"`
	// Error contains error message if execution failed
	Error string `json:"error,omitempty"`
	// Duration is the total execution time
	Duration time.Duration `json:"duration"`
}

// ToolCallRecord represents a single tool call made during PTC execution
type ToolCallRecord struct {
	// ToolName is the name of the tool called
	ToolName string `json:"tool_name"`
	// Arguments passed to the tool
	Arguments map[string]interface{} `json:"arguments"`
	// Result returned by the tool
	Result interface{} `json:"result"`
	// Error if the tool call failed
	Error string `json:"error,omitempty"`
	// Duration of the tool call
	Duration time.Duration `json:"duration"`
}

// ToolHandler is the function signature for tool handlers
type ToolHandler func(ctx context.Context, args map[string]interface{}) (interface{}, error)

// SearchProvider provides on-demand tool discovery and execution
type SearchProvider interface {
	SearchAndExecute(ctx context.Context, query string, instruction string, scope string) (interface{}, error)
}

// SandboxRuntime defines the interface for sandbox runtimes
type SandboxRuntime interface {
	// Type returns the runtime type
	Type() RuntimeType
	// Execute runs code in the sandbox
	Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error)
	// RegisterTool registers a tool handler
	RegisterTool(name string, handler ToolHandler) error
	// UnregisterTool removes a tool handler
	UnregisterTool(name string) error
	// ListTools returns all registered tool names
	ListTools() []string
	// SetSearchProvider sets the search provider for on-demand tool discovery
	SetSearchProvider(provider SearchProvider)
	// Close releases resources
	Close() error
}

// ToolRouter routes tool calls to appropriate handlers
type ToolRouter interface {
	// Route routes a tool call to the appropriate handler
	Route(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
	// ListAvailableTools returns all available tools
	ListAvailableTools(ctx context.Context) ([]ToolInfo, error)
	// GetToolInfo returns information about a specific tool
	GetToolInfo(ctx context.Context, name string) (*ToolInfo, error)
}

// ToolInfo contains information about a tool
type ToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
	Category    string                 `json:"category,omitempty"` // rag, mcp, skill, custom
}

// ExecutionHistory stores execution history for debugging/auditing
type ExecutionHistory struct {
	ID         string            `json:"id"`
	Request    *ExecutionRequest `json:"request"`
	Result     *ExecutionResult  `json:"result"`
	ExecutedAt time.Time         `json:"executed_at"`
}

// ExecutionStore stores execution history
type ExecutionStore interface {
	// Save saves an execution record
	Save(ctx context.Context, history *ExecutionHistory) error
	// Get retrieves an execution by ID
	Get(ctx context.Context, id string) (*ExecutionHistory, error)
	// List lists executions with optional filtering
	List(ctx context.Context, limit int) ([]*ExecutionHistory, error)
	// Delete removes old executions
	Delete(ctx context.Context, before time.Time) error
}
