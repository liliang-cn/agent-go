package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Reading a tool's own verdict out of its result.
//
// A tool call has two ways to fail and the runtime only ever saw one. The Go
// error is the call failing: the server is down, the arguments are wrong. But
// most tools that do work return normally and put the failure in the result —
// a shell command that exited 1, an MCP tool with isError set, a handler that
// answers {"ok": false, "error": …}. To the trace, the tally and the
// observers those were successes: "tool bash dur=0.21s", whether the script
// printed its report or a traceback.
//
// toolOutcome reads the result for what the tool itself says happened. It
// looks at the result's structure only — the handful of field names tools
// already use to say "this failed" and "this is the output" — never at which
// tool it was or what the output is about. A tool that reports nothing in any
// of those fields is taken at its word: no error, no failure.

// failureFlags are the fields by which a result says it failed: a false
// "ok"/"success", or a true "is_error"/"isError"/"failed".
var (
	okFields     = []string{"ok", "success", "succeeded"}
	failedFields = []string{"is_error", "isError", "failed", "timed_out"}
	exitFields   = []string{"exit_code", "exitCode", "returncode", "return_code", "status_code"}
	errorFields  = []string{"error", "err", "stderr", "message"}
	outputFields = []string{"stdout", "output", "text", "content", "result"}
	nestFields   = []string{"data", "result"}
)

// toolOutcome reports whether a tool's result says the call failed, and the
// short reason for the trace line: the exit status when there is one, then
// the end of whatever the tool wrote about it.
func toolOutcome(res interface{}) (failed bool, note string) {
	m := resultAsMap(res)
	if m == nil {
		return false, ""
	}
	// One level of wrapping is common: {"ok": false, "data": {"exit_code": 1, …}}.
	layers := []map[string]interface{}{m}
	for _, key := range nestFields {
		if inner, ok := m[key].(map[string]interface{}); ok {
			layers = append(layers, inner)
		}
	}

	var parts []string
	for _, layer := range layers {
		for _, key := range okFields {
			if v, ok := layer[key].(bool); ok && !v {
				failed = true
			}
		}
		for _, key := range failedFields {
			if v, ok := layer[key].(bool); ok && v {
				failed = true
				if key == "timed_out" {
					parts = append(parts, "timed out")
				}
			}
		}
		for _, key := range exitFields {
			if code, ok := asNumber(layer[key]); ok && code != 0 {
				// An HTTP-style status only counts as failure from 400 up.
				if key == "status_code" && code < 400 {
					continue
				}
				failed = true
				parts = append(parts, fmt.Sprintf("exit=%d", int64(code)))
			}
		}
	}
	if !failed {
		return false, ""
	}

	// Why: what the tool wrote to its error channel, else the end of its
	// output — a program that fails says so last. The innermost layer first:
	// the wrapper's "error" is usually a summary of what is inside.
	for i := len(layers) - 1; i >= 0; i-- {
		if text := firstText(layers[i], errorFields); text != "" {
			parts = append(parts, trace.Tail(text, 3))
			return true, strings.Join(parts, " ")
		}
	}
	for i := len(layers) - 1; i >= 0; i-- {
		if text := firstText(layers[i], outputFields); text != "" {
			parts = append(parts, trace.Tail(text, 3))
			break
		}
	}
	return true, strings.Join(parts, " ")
}

// resultAsMap is the result as a JSON object, or nil when it is not one. A
// typed result (an MCP call's struct) is read through its JSON form, so its
// fields are found under the names it would be sent with.
func resultAsMap(res interface{}) map[string]interface{} {
	switch v := res.(type) {
	case nil:
		return nil
	case map[string]interface{}:
		return v
	case string:
		t := strings.TrimSpace(v)
		if !strings.HasPrefix(t, "{") {
			return nil
		}
		var m map[string]interface{}
		if json.Unmarshal([]byte(t), &m) != nil {
			return nil
		}
		return m
	case []byte, bool, int, int64, float64:
		return nil
	}
	raw, err := json.Marshal(res)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func asNumber(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// firstText is the first of the named fields that holds text.
func firstText(m map[string]interface{}, keys []string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		case []interface{}:
			// MCP content: [{"type": "text", "text": "…"}, …].
			var b strings.Builder
			for _, item := range v {
				if part, ok := item.(map[string]interface{}); ok {
					if t, ok := part["text"].(string); ok {
						b.WriteString(t)
						b.WriteByte('\n')
					}
				}
			}
			if strings.TrimSpace(b.String()) != "" {
				return b.String()
			}
		}
	}
	return ""
}
