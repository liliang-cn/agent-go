package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func asst(content string, ids ...string) domain.Message {
	m := domain.Message{Role: "assistant", Content: content}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, domain.ToolCall{ID: id, Type: "function"})
	}
	return m
}
func toolMsg(id, content string) domain.Message {
	return domain.Message{Role: "tool", ToolCallID: id, Content: content}
}

func TestSanitizeToolPairing(t *testing.T) {
	// Valid, fully-paired history is returned unchanged (same length).
	valid := []domain.Message{
		{Role: "user", Content: "hi"},
		asst("", "c1"),
		toolMsg("c1", "ok"),
		{Role: "assistant", Content: "done"},
	}
	if got := sanitizeToolPairing(valid); len(got) != len(valid) {
		t.Fatalf("valid history changed: got %d want %d", len(got), len(valid))
	}

	// Orphaned tool result (no matching assistant tool_call) is dropped.
	orphanResult := []domain.Message{
		{Role: "user", Content: "hi"},
		toolMsg("ghost", "stale result"), // no assistant call -> drop
		{Role: "assistant", Content: "answer"},
	}
	got := sanitizeToolPairing(orphanResult)
	for _, m := range got {
		if m.Role == "tool" {
			t.Fatalf("orphaned tool result was not dropped: %+v", m)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}

	// Assistant tool_call with no result -> that call is stripped; pure tool-call
	// message with all calls stripped is dropped.
	orphanCall := []domain.Message{
		{Role: "user", Content: "hi"},
		asst("", "c1", "c2"), // only c1 answered
		toolMsg("c1", "ok"),
		asst("", "c3"), // never answered -> whole message dropped
		{Role: "assistant", Content: "final"},
	}
	got = sanitizeToolPairing(orphanCall)
	// find the assistant with tool calls
	var toolcallMsgs, toolResults int
	for _, m := range got {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			toolcallMsgs++
			for _, tc := range m.ToolCalls {
				if tc.ID != "c1" {
					t.Fatalf("unexpected surviving tool call %s", tc.ID)
				}
			}
		}
		if m.Role == "tool" {
			toolResults++
		}
	}
	if toolcallMsgs != 1 || toolResults != 1 {
		t.Fatalf("toolcallMsgs=%d toolResults=%d, want 1/1", toolcallMsgs, toolResults)
	}

	// No tools at all -> unchanged.
	plain := []domain.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}
	if got := sanitizeToolPairing(plain); len(got) != 2 {
		t.Fatalf("plain changed")
	}
}
