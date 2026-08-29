package pool

import (
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Explicit prompt-cache breakpoints.
//
// An agent loop sends its whole conversation every round. On a provider that
// caches only what is marked, that means paying full prefill for the entire
// history on every single round — survivable for a chat turn, ruinous for a
// run that goes on for hours.
//
// Two marks fix it:
//
//   - The prefix. System prompt and tool schemas are the same bytes on every
//     iteration of a run, and they are the largest fixed cost in the loop.
//   - The tail of the history. The next round's history is this one plus a few
//     appended messages, so a mark at the end means the next round finds
//     everything up to here already warm and prefills only what is new.
//
// Two is also the right ceiling. Providers cap the number of breakpoints
// (Anthropic allows four) and each one costs a cache write, so marking every
// message would pay the surcharge repeatedly for entries read once.
//
// This mirrors harness-rs's Anthropic adapter, including where the prefix mark
// goes: the request is ordered system, then tools, then messages, so marking
// the *last tool* caches the tool schemas and the system prompt before them in
// one breakpoint. The system message is marked only when there are no tools to
// carry it.

// maxPromptCacheBreakpoints is how many marks one request carries: the prefix
// and the tail of the history.
const maxPromptCacheBreakpoints = 2

// promptCacheControl is the marker. "ephemeral" — roughly a five-minute
// lifetime — is the only one the OpenAI-shaped passthrough offers, and it is
// the right one for a loop whose rounds are seconds apart.
func promptCacheControl() map[string]interface{} {
	return map[string]interface{}{"type": "ephemeral"}
}

// markPromptCacheBreakpoints marks the prefix and the tail of the history,
// and reports how many marks it placed.
//
// Everything it cannot mark it leaves exactly as it was, so a request that
// gains no breakpoint is byte-identical to one built with caching off: turning
// this on can fail to help, but it cannot make a request worse.
func markPromptCacheBreakpoints(apiMessages, apiTools []map[string]interface{}) int {
	marked := 0

	// The prefix. One mark covers system + tools when it sits on the last
	// tool; without tools there is nothing after the system messages, so the
	// last of those carries it instead.
	prefixMsgIdx := -1
	if len(apiTools) > 0 {
		apiTools[len(apiTools)-1]["cache_control"] = promptCacheControl()
		marked++
	} else {
		for i, msg := range apiMessages {
			if role, _ := msg["role"].(string); role != "system" {
				break
			}
			prefixMsgIdx = i
		}
		if prefixMsgIdx >= 0 && markPromptCacheBreakpoint(apiMessages[prefixMsgIdx]) {
			marked++
		} else {
			prefixMsgIdx = -1
		}
	}

	// The tail. Walk back to the last message with text to attach to —
	// never the message that already carries the prefix mark, since one
	// breakpoint in one place is one breakpoint.
	for i := len(apiMessages) - 1; i > prefixMsgIdx; i-- {
		if markPromptCacheBreakpoint(apiMessages[i]) {
			marked++
			break
		}
	}

	return marked
}

// markPromptCacheBreakpoint converts one message's string content into a
// single text block carrying the marker, and reports whether it could.
//
// Only a non-empty string qualifies. That skips the assistant turns that
// carry tool calls and no text — there is no text block to attach to — and
// the search simply walks back to the tool result before them, a breakpoint
// one message earlier and worth just as much. It also skips anything already
// converted, so the prefix mark can never be overwritten by the tail mark.
func markPromptCacheBreakpoint(msg map[string]interface{}) bool {
	if msg == nil {
		return false
	}
	text, ok := msg["content"].(string)
	if !ok || text == "" {
		return false
	}
	msg["content"] = []interface{}{
		map[string]interface{}{
			"type":          "text",
			"text":          text,
			"cache_control": promptCacheControl(),
		},
	}
	return true
}

// shouldRetryPoolWithoutPromptCache reports whether the upstream rejected the
// cache markers. Servers that have never heard of cache_control answer with
// the same "unsupported / invalid / unknown field" family as every other
// optional parameter, so this reads like its siblings: strip the field, retry
// once, and let the run continue uncached rather than fail.
func shouldRetryPoolWithoutPromptCache(opts *domain.GenerationOptions, err error) bool {
	if opts == nil || opts.PromptCache == domain.PromptCacheOff || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// The marker rides inside the content blocks and the tool entries, so a
	// server that dislikes it complains about the field itself or about the
	// shape it arrived in.
	if !strings.Contains(msg, "cache_control") &&
		!strings.Contains(msg, "content") &&
		!strings.Contains(msg, "tools") {
		return false
	}
	return strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "does not support") ||
		strings.Contains(msg, "invalid") ||
		strings.Contains(msg, "unknown")
}
