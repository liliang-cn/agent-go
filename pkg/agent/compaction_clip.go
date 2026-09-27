package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Compaction's two model-free halves: clipping old tool results before a
// summary is asked for, and a mechanical summary for when the summariser
// cannot be reached.
//
// Most of a tool-using agent's history is tool output — a file read, a test
// log, a directory listing — and most of that output was needed for one or two
// rounds and never again. Summarising it costs a model call, and the call is
// the part that fails: a reasoning model that spends its budget thinking, a
// gateway that times out. Replacing old output with a one-line stub costs
// nothing, cannot fail, keeps every call id and every call/result pair where
// it was, and on a coding agent is usually enough on its own.

const (
	// clipMinChars is the smallest tool result worth replacing. A stub is
	// about 250 characters; replacing a result shorter than a few of those
	// saves nothing and throws away the whole of it.
	clipMinChars = 800
	// clipStubMarker opens every stub, so a stub is never clipped again and a
	// reader can tell one from real output.
	clipStubMarker = "[clipped tool result]"
	// clipLineChars bounds the first/last line quoted in a stub.
	clipLineChars = 120
	// clipArgsChars bounds the argument summary in a stub.
	clipArgsChars = 160

	// fallbackSummaryChars is the budget for a model-free summary.
	fallbackSummaryChars = 6000
)

// clipOldToolResults replaces the content of tool results older than the most
// recent keepRounds tool rounds with a one-line stub. A tool round is one
// assistant message carrying tool calls; its results are the tool messages
// answering those calls. Nothing before protectEnd is touched.
//
// The message count, roles, call ids and tool names are unchanged — the
// output pairs exactly as the input did — so it is safe on every provider
// that validates tool pairing. The input slice is not modified. The returned
// count is how many results were replaced; zero means out is msgs.
//
// Stubs are deterministic and a stub is never clipped again, so the rewritten
// history is byte-stable from here on: a prompt cache that breaks at the first
// clipped message re-forms behind it on the next round.
func clipOldToolResults(msgs []domain.Message, protectEnd, keepRounds int) ([]domain.Message, int) {
	if keepRounds < 0 {
		return msgs, 0
	}
	// Find the assistant message that opens the oldest round still kept
	// verbatim; everything before it is eligible.
	boundary := len(msgs)
	rounds := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			if rounds == keepRounds {
				break
			}
			rounds++
			boundary = i
		}
	}
	if keepRounds == 0 {
		boundary = len(msgs)
	}

	calls := map[string]domain.ToolCall{}
	for _, m := range msgs[:boundary] {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				calls[tc.ID] = tc
			}
		}
	}

	var out []domain.Message
	clipped := 0
	for i := protectEnd; i < boundary; i++ {
		m := msgs[i]
		if m.Role != "tool" || len(m.Parts) > 0 || len(m.Content) < clipMinChars ||
			strings.HasPrefix(m.Content, clipStubMarker) {
			continue
		}
		if out == nil {
			out = append([]domain.Message(nil), msgs...)
		}
		tc := calls[m.ToolCallID]
		out[i].Content = toolResultStub(tc.Function.Name, tc.Function.Arguments, m.Content)
		clipped++
	}
	if clipped == 0 {
		return msgs, 0
	}
	return out, clipped
}

// toolResultStub is the one line an old tool result becomes: what was called,
// how big the answer was, how it began and ended, and how to get it back.
func toolResultStub(name string, args map[string]interface{}, content string) string {
	if name == "" {
		name = "tool"
	}
	lines := strings.Count(content, "\n") + 1
	first, last := firstAndLastLine(content)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s(%s) returned %d chars, %d lines.", clipStubMarker, name, summarizeArgs(args, clipArgsChars), len(content), lines)
	if first != "" {
		fmt.Fprintf(&b, " First: %q", first)
	}
	if last != "" && last != first {
		fmt.Fprintf(&b, " Last: %q", last)
	}
	b.WriteString(" Call it again if you need the full output.")
	return b.String()
}

func firstAndLastLine(s string) (string, string) {
	var first, last string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		last = line
	}
	return truncateRunes(first, clipLineChars), truncateRunes(last, clipLineChars)
}

// summarizeArgs renders call arguments short and in sorted key order, so the
// same call always produces the same stub.
func summarizeArgs(args map[string]interface{}, max int) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var v string
		switch t := args[k].(type) {
		case string:
			v = fmt.Sprintf("%q", truncateRunes(oneLine(t, 0), 60))
		default:
			raw, err := json.Marshal(t)
			if err != nil {
				v = "…"
			} else {
				v = truncateRunes(string(raw), 60)
			}
		}
		parts = append(parts, k+"="+v)
	}
	out := strings.Join(parts, ", ")
	if len(out) > max {
		out = truncateRunes(out, max) + "…"
	}
	return out
}

// fallbackSummaryInput is what a model-free summary is built from. Every field
// is something the runtime already holds; nothing is inferred.
type fallbackSummaryInput struct {
	Goal   string
	Plan   string
	Middle []domain.Message
	Budget int
}

// buildFallbackSummary assembles a compaction summary without a model: the
// goal, the plan with its notes, the files written, the calls made and the
// last error seen in the folded slice. It is thinner than a written summary
// and it always succeeds, which is the property that matters when the
// summariser has already failed twice.
func buildFallbackSummary(in fallbackSummaryInput) string {
	budget := in.Budget
	if budget <= 0 {
		budget = fallbackSummaryChars
	}
	var files []string
	seenFile := map[string]bool{}
	var actions []string
	lastError := ""
	lastAssistant := ""
	for _, m := range in.Middle {
		switch m.Role {
		case "assistant":
			if t := strings.TrimSpace(m.Content); t != "" {
				lastAssistant = t
			}
			for _, tc := range m.ToolCalls {
				actions = append(actions, tc.Function.Name+"("+summarizeArgs(tc.Function.Arguments, 100)+")")
				if !filesystemWriteTools[tc.Function.Name] {
					continue
				}
				for _, key := range []string{"path", "file_path", "filename", "destination", "target"} {
					if p, ok := tc.Function.Arguments[key].(string); ok && strings.TrimSpace(p) != "" && !seenFile[p] {
						seenFile[p] = true
						files = append(files, p)
					}
				}
			}
		case "tool":
			if idx := strings.Index(m.Content, "Error: "); idx >= 0 && (idx == 0 || m.Content[idx-1] == '\n') {
				lastError = truncateRunes(oneLine(m.Content[idx:], 0), 400)
			}
		}
	}

	var b strings.Builder
	section := func(title, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		b.WriteString(title)
		b.WriteString(":\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	section("Goal", truncateRunes(in.Goal, budget/4))
	section("Plan", truncateRunes(in.Plan, budget/4))
	if len(files) > 0 {
		section("Files written", "- "+strings.Join(files, "\n- "))
	}
	if lastError != "" {
		section("Last error", lastError)
	}
	if lastAssistant != "" {
		section("Last note from the assistant", truncateRunes(lastAssistant, 600))
	}
	if len(actions) > 0 {
		// Most recent last, and when there is no room for all of them, the
		// most recent are the ones kept.
		room := budget - b.Len() - 64
		var kept []string
		used := 0
		for i := len(actions) - 1; i >= 0 && room > 0; i-- {
			if used+len(actions[i])+3 > room {
				break
			}
			kept = append([]string{actions[i]}, kept...)
			used += len(actions[i]) + 3
		}
		if len(kept) > 0 {
			header := fmt.Sprintf("Tool calls (%d, last %d shown)", len(actions), len(kept))
			section(header, "- "+strings.Join(kept, "\n- "))
		}
	}
	return truncateRunes(b.String(), budget)
}
