package handler

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/mcp"
)

type fakeDirectChatLLM struct {
	results []*domain.GenerationResult
	calls   [][]domain.Message
}

func (f *fakeDirectChatLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	cloned := make([]domain.Message, len(messages))
	copy(cloned, messages)
	f.calls = append(f.calls, cloned)

	index := len(f.calls) - 1
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	return f.results[index], nil
}

func TestRunDirectLLMToolLoopExecutesToolAndContinues(t *testing.T) {
	llm := &fakeDirectChatLLM{
		results: []*domain.GenerationResult{
			{
				ToolCalls: []domain.ToolCall{
					{
						Type: "function",
						Function: domain.FunctionCall{
							Name:      "mcp_filesystem_read_file",
							Arguments: map[string]interface{}{"path": "/tmp/demo.txt"},
						},
					},
				},
			},
			{
				Content: "Final answer from tool output.",
			},
		},
	}

	finalResult, executions, err := runDirectLLMToolLoop(
		context.Background(),
		llm,
		[]domain.Message{{Role: "user", Content: "read the file"}},
		[]domain.ToolDefinition{{
			Type: "function",
			Function: domain.ToolFunction{
				Name:        "mcp_filesystem_read_file",
				Description: "Read a file",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		}},
		&domain.GenerationOptions{},
		4,
		func(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
			return &mcp.ToolResult{
				Success: true,
				Data:    map[string]interface{}{"content": "hello"},
			}, nil
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("runDirectLLMToolLoop returned error: %v", err)
	}
	if finalResult == nil || finalResult.Content != "Final answer from tool output." {
		t.Fatalf("unexpected final result: %#v", finalResult)
	}
	if len(executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(executions))
	}
	if executions[0].ToolCallID == "" {
		t.Fatalf("expected normalized tool call id, got empty")
	}
	if len(llm.calls) != 2 {
		t.Fatalf("expected 2 llm calls, got %d", len(llm.calls))
	}
	secondCall := llm.calls[1]
	if len(secondCall) != 3 {
		t.Fatalf("expected second llm call to include assistant and tool messages, got %#v", secondCall)
	}
	if secondCall[1].Role != "assistant" || len(secondCall[1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call message, got %#v", secondCall[1])
	}
	if secondCall[2].Role != "tool" || secondCall[2].ToolCallID == "" {
		t.Fatalf("expected tool result message with tool_call_id, got %#v", secondCall[2])
	}
}

func TestBuildDirectChatToolDefinitionsFiltersSelectedTools(t *testing.T) {
	toolDefs, armed := buildDirectChatToolDefinitions([]mcp.AgentToolInfo{
		{
			Name:        "mcp_server_alpha",
			Description: "Alpha tool",
			InputSchema: map[string]interface{}{"type": "object"},
		},
		{
			Name:        "mcp_server_beta",
			Description: "Beta tool",
		},
	}, []string{"mcp_server_beta"})

	if len(toolDefs) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(toolDefs))
	}
	if len(armed) != 1 || armed[0] != "mcp_server_beta" {
		t.Fatalf("unexpected armed tools: %#v", armed)
	}
	if toolDefs[0].Function.Name != "mcp_server_beta" {
		t.Fatalf("unexpected tool name: %#v", toolDefs[0])
	}
	if toolDefs[0].Function.Parameters == nil {
		t.Fatalf("expected fallback schema, got nil")
	}
}
