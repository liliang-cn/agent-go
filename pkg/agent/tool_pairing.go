package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// sanitizeToolPairing returns a message slice in which every tool-result message
// has a matching assistant tool_call and vice-versa. Strict provider backends
// (OpenAI Responses / Codex, Anthropic, …) reject a request that contains a
// "function_call_output" with no matching "function_call" (e.g. "No tool call
// found for function call output with call_id X"). Such orphans appear when
// replayed session history or context compaction drops an assistant tool_call
// message while keeping its tool result (or drops a result while keeping the
// call).
//
// It is a no-op (returns the input unchanged) when everything is already paired,
// so the common live-turn path pays only two cheap scans.
//
//   - a tool message survives only if its ToolCallID is also present as some
//     assistant ToolCall.ID;
//   - an assistant ToolCall survives only if a tool result with that ID exists;
//   - an assistant message that ends up with no tool calls and no content is
//     dropped (it was a pure, now-empty tool-call message).
func sanitizeToolPairing(messages []domain.Message) []domain.Message {
	resultIDs := make(map[string]bool)
	for i := range messages {
		if messages[i].Role == "tool" && messages[i].ToolCallID != "" {
			resultIDs[messages[i].ToolCallID] = true
		}
	}
	callIDs := make(map[string]bool)
	for i := range messages {
		if messages[i].Role == "assistant" {
			for _, tc := range messages[i].ToolCalls {
				callIDs[tc.ID] = true
			}
		}
	}
	if len(resultIDs) == 0 && len(callIDs) == 0 {
		return messages
	}

	paired := make(map[string]bool, len(resultIDs))
	allPaired := true
	for id := range resultIDs {
		if callIDs[id] {
			paired[id] = true
		} else {
			allPaired = false
		}
	}
	for id := range callIDs {
		if !resultIDs[id] {
			allPaired = false
		}
	}
	if allPaired {
		return messages // fast path: nothing to fix
	}

	out := make([]domain.Message, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				kept := make([]domain.ToolCall, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					if paired[tc.ID] {
						kept = append(kept, tc)
					}
				}
				if len(kept) == 0 && strings.TrimSpace(m.Content) == "" && len(m.Parts) == 0 {
					continue // pure tool-call message that lost all its calls
				}
				m.ToolCalls = kept
			}
			out = append(out, m)
		case "tool":
			if paired[m.ToolCallID] {
				out = append(out, m)
			}
		default:
			out = append(out, m)
		}
	}
	return out
}
