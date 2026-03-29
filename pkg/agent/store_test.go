package agent

import (
	"path/filepath"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestStoreSessionPreservesToolCallingMetadata(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	session := NewSessionWithID("session-1", "agent-1")
	session.AddMessage(domain.Message{
		Role:    "user",
		Content: "hi",
	})
	session.AddMessage(domain.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []domain.ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: domain.FunctionCall{
					Name: "route_builtin_request",
					Arguments: map[string]interface{}{
						"prompt": "hi",
					},
				},
			},
		},
		ResponseID: "resp_1",
	})
	session.AddMessage(domain.Message{
		Role:       "tool",
		Content:    `{"result":"Hi! How can I help?"}`,
		ToolCallID: "call_1",
	})

	if err := store.SaveSession(session); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	loaded, err := store.GetSession(session.GetID())
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}

	if len(loaded.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(loaded.Messages))
	}

	assistant := loaded.Messages[1]
	if assistant.ResponseID != "resp_1" {
		t.Fatalf("expected response id to round-trip, got %q", assistant.ResponseID)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected tool call id to round-trip, got %q", assistant.ToolCalls[0].ID)
	}
	if assistant.ToolCalls[0].Function.Name != "route_builtin_request" {
		t.Fatalf("expected tool call name to round-trip, got %q", assistant.ToolCalls[0].Function.Name)
	}

	toolResult := loaded.Messages[2]
	if toolResult.ToolCallID != "call_1" {
		t.Fatalf("expected tool result to keep tool_call_id, got %q", toolResult.ToolCallID)
	}
}
