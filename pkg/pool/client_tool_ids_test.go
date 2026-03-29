package pool

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestBuildPoolGenerateWithToolsRequestNormalizesToolCallIDs(t *testing.T) {
	req := buildPoolGenerateWithToolsRequest("gpt-test", []domain.Message{
		{
			Role:    "assistant",
			Content: "Calling tool",
			ToolCalls: []domain.ToolCall{
				{
					ID:   "call_old_1",
					Type: "function",
					Function: domain.FunctionCall{
						Name:      "lookup",
						Arguments: map[string]interface{}{"q": "gold"},
					},
				},
			},
		},
		{
			Role:       "tool",
			Content:    "done",
			ToolCallID: "call_old_1",
		},
	}, nil, nil)

	messages, ok := req["messages"].([]map[string]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("unexpected messages payload: %#v", req["messages"])
	}

	toolCalls, ok := messages[0]["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("unexpected tool_calls payload: %#v", messages[0]["tool_calls"])
	}
	if toolCalls[0]["id"] != "fc_call_old_1" {
		t.Fatalf("expected normalized assistant tool call id, got %#v", toolCalls[0]["id"])
	}
	if toolCalls[0]["call_id"] != "fc_call_old_1" {
		t.Fatalf("expected call_id alias for assistant tool call, got %#v", toolCalls[0]["call_id"])
	}
	if messages[1]["tool_call_id"] != "fc_call_old_1" {
		t.Fatalf("expected normalized tool message id, got %#v", messages[1]["tool_call_id"])
	}
	if messages[1]["call_id"] != "fc_call_old_1" {
		t.Fatalf("expected call_id alias for tool message, got %#v", messages[1]["call_id"])
	}
}
