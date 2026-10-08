package pool

import (
	"strings"
)

// Reasoning effort.
//
// A reasoning model thinks before every call, and on a chat turn that is
// most of the wait: gemini-3.8-flash-high behind an OpenAI-compatible
// gateway took 5.2–8.3s to its first tool call on "what is on today?", and
// 3.9–4.7s with reasoning_effort "low", choosing the same tool. A provider
// configured with an effort sends it on every chat completion, unless the
// call already says how to reason (a "thinking" switch, or its own effort).
// An upstream that refuses the field is asked once and then no more.

// SetReasoningEffort sets the reasoning_effort this client sends: "low",
// "medium", "high", or whatever the upstream accepts ("minimal", "none").
// Empty sends nothing, which is the default.
func (c *Client) SetReasoningEffort(level string) {
	c.reasoningEffort = strings.TrimSpace(level)
}

// withEffort is body with the client's reasoning_effort added, when there is
// one to add. The caller's map is left as it was.
func (c *Client) withEffort(path string, body interface{}) (interface{}, bool) {
	if c.reasoningEffort == "" || c.effortRejected.Load() || path != "/chat/completions" {
		return body, false
	}
	m, ok := body.(map[string]interface{})
	if !ok {
		return body, false
	}
	if _, set := m["reasoning_effort"]; set {
		return body, false
	}
	if _, set := m["thinking"]; set {
		return body, false
	}
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out["reasoning_effort"] = c.reasoningEffort
	return out, true
}

// refusedEffort reports whether err is the upstream refusing the field; the
// client then stops sending it.
func (c *Client) refusedEffort(err error) bool {
	if err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
		return false
	}
	c.effortRejected.Store(true)
	return true
}
