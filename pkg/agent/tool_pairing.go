package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// sanitizeToolPairing returns a message slice in which every tool-result message
// answers a tool_call of the assistant message just before its run of results,
// and every assistant tool_call has its result there. Strict provider backends
// (OpenAI Responses / Codex, Anthropic, chat-completions proxies, …) reject
// anything else: "No tool call found for function call output with call_id X",
// or "'tool_call_id' of X not found in 'tool_calls' of previous message".
//
// Orphans come from replayed session history: compaction drops a tool_call
// message and keeps its result, or a result is persisted twice and the second
// copy lands after a message steered into the run. Being paired somewhere in
// the history is not enough — the result has to sit right after its call —
// so pairing is decided by position:
//
//   - a tool message survives only inside the run of tool messages directly
//     after an assistant message, answering one of that message's calls, and
//     only the first time that call is answered there;
//   - an assistant ToolCall survives only if it is answered that way;
//   - an assistant message that ends up with no tool calls and no content is
//     dropped (it was a pure, now-empty tool-call message).
//
// It is a no-op (returns the input unchanged) when everything is already in
// place, so the common live-turn path pays one scan.
func sanitizeToolPairing(messages []domain.Message) []domain.Message {
	// For each assistant message with calls: which of them are answered in
	// the run of tool messages right after it. keep marks the tool messages
	// that do the answering.
	answered := make(map[int]map[string]bool)
	keep := make(map[int]bool)
	clean := true
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		if m.Role == "tool" {
			if !keep[i] {
				clean = false // not in the run after any call
			}
			continue
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		calls := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = true
		}
		got := make(map[string]bool, len(calls))
		for j := i + 1; j < len(messages) && messages[j].Role == "tool"; j++ {
			id := messages[j].ToolCallID
			if calls[id] && !got[id] {
				got[id] = true
				keep[j] = true
			}
		}
		if len(got) != len(calls) {
			clean = false
		}
		answered[i] = got
	}
	if clean {
		return messages // fast path: nothing to fix
	}

	out := make([]domain.Message, 0, len(messages))
	for i, m := range messages {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				kept := make([]domain.ToolCall, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					if answered[i][tc.ID] {
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
			if keep[i] {
				out = append(out, m)
			}
		default:
			out = append(out, m)
		}
	}
	return out
}
