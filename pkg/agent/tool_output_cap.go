package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// One cap for every tool result on its way into the conversation.
//
// A tool result is written into history once and re-sent with every later
// round, so one oversized result is paid for on every turn that follows it —
// and until compaction runs, it is paid in full. Before this there was no
// bound at all outside bash's own 4000-char tail: a 10,000-line fs_read put
// about a megabyte (~440k estimated tokens) into the next request. MCP tools,
// skills and sub-agents had nothing either.
//
// The cap sits at the single point results become tool messages
// (appendToolRoundToMessages), so it applies to built-ins, MCP, skills and
// sub-agents alike. It keeps the head and the tail and replaces the middle
// with a line that says how much was cut and how to get it — an error or a
// summary is usually at one end, and the model has to know something is
// missing rather than guess.
//
// What it deliberately does not touch:
//   - the tool-call id: the capped message answers the same call;
//   - the host's copy: events, observers, hooks and the terminal answer of a
//     task_complete all see the full result — only what the model reads is
//     bounded;
//   - the prompt prefix: the cut is a pure function of the result, and the
//     message is written once and never rewritten, so every later round
//     re-sends the same bytes.

// DefaultToolOutputLimit is the most a single tool result may put into the
// conversation, in bytes of the rendered tool message. About 8k tokens: enough
// for a page of a file, a test run's failures or a search listing, and small
// enough that a long run can afford dozens of them.
const DefaultToolOutputLimit = 32000

// minStructuredLeafBytes is the smallest a single string field is cut to
// while keeping a structured result valid JSON. Below it the field would be
// all marker and no content, so the whole result is cut as text instead.
const minStructuredLeafBytes = 512

// WithToolOutputLimit sets the cap on one tool result as the model sees it,
// in bytes. 0 keeps DefaultToolOutputLimit; a negative value turns the cap
// off, which restores the old behaviour of sending every result whole.
//
// The cap applies to every tool — built-ins, MCP, skills, sub-agents. A cut
// is never silent: the model reads a line saying how much was omitted, and
// observers implementing ToolOutputObserver (TraceWriter and ActivityLog
// among them) hear about it.
func (b *Builder) WithToolOutputLimit(n int) *Builder {
	b.toolOutputLimit = n
	return b
}

// toolOutputCap resolves the effective limit: 0 means no cap.
func (s *Service) toolOutputCap() int {
	if s == nil {
		return DefaultToolOutputLimit
	}
	switch {
	case s.toolOutputLimit < 0:
		return 0
	case s.toolOutputLimit == 0:
		return DefaultToolOutputLimit
	default:
		return s.toolOutputLimit
	}
}

// ToolOutputTruncation describes one tool result the cap cut down.
type ToolOutputTruncation struct {
	TaskID     string
	RunID      string
	SessionID  string
	AgentName  string
	Round      int
	ToolName   string
	ToolCallID string
	// OriginalBytes is the rendered result before the cut; KeptBytes is what
	// the model received, marker included.
	OriginalBytes int
	KeptBytes     int
	Limit         int
	// StructureKept is true when a JSON result was cut inside its string
	// fields and is still valid JSON; false when it was cut as plain text.
	StructureKept bool
}

// ToolOutputObserver hears about every tool result the cap cut down.
//
// An optional interface, not a method on Observer, for the reason
// ResourceObserver gives: hosts implement Observer in full, and a new method
// would break every one that does not embed BaseObserver.
type ToolOutputObserver interface {
	OnToolOutputTruncated(ctx context.Context, info ToolOutputTruncation)
}

// capToolResult bounds one rendered tool result. res is the value the tool
// returned (after image extraction) and text is its rendering; the returned
// string is what goes into the tool message. ok is false when nothing was cut.
func capToolResult(res interface{}, text string, limit int) (string, ToolOutputTruncation, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, ToolOutputTruncation{}, false
	}
	info := ToolOutputTruncation{OriginalBytes: len(text), Limit: limit}
	if out, ok := capStructured(res, text, limit); ok {
		info.KeptBytes, info.StructureKept = len(out), true
		return out, info, true
	}
	out := truncateMiddle(text, limit, textOmissionMarker)
	info.KeptBytes = len(out)
	return out, info, true
}

// capStructured cuts a JSON result inside its string fields, so a result
// like {"ok":true,"data":{"content":"<1MB>","next_offset":230}} keeps its
// shape and its small fields and loses only the middle of the big one. It
// finds the largest per-field length that fits by bisection; a result whose
// bulk is not in strings (a huge array of small items) cannot fit this way and
// falls back to a text cut.
func capStructured(res interface{}, text string, limit int) (string, bool) {
	switch res.(type) {
	case string, nil, []byte:
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	longest := longestString(v)
	if longest <= minStructuredLeafBytes {
		return "", false
	}
	render := func(leaf int) (string, bool) {
		b, err := marshalNoEscape(capStrings(v, leaf))
		if err != nil {
			return "", false
		}
		return b, len(b) <= limit
	}
	best, found := "", false
	lo, hi := minStructuredLeafBytes, longest-1
	if hi > limit {
		hi = limit
	}
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if out, fits := render(mid); fits {
			best, found = out, true
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, found
}

// marshalNoEscape encodes like json.Marshal but leaves <, > and & alone:
// escaping them triples the size of any HTML or shell output for no reader's
// benefit, and would make the cut result bigger than it looks.
func marshalNoEscape(v interface{}) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func longestString(v interface{}) int {
	switch t := v.(type) {
	case string:
		return len(t)
	case map[string]interface{}:
		m := 0
		for _, x := range t {
			if n := longestString(x); n > m {
				m = n
			}
		}
		return m
	case []interface{}:
		m := 0
		for _, x := range t {
			if n := longestString(x); n > m {
				m = n
			}
		}
		return m
	}
	return 0
}

// capStrings returns a copy of v with every string longer than leaf cut in
// the middle to leaf bytes.
func capStrings(v interface{}, leaf int) interface{} {
	switch t := v.(type) {
	case string:
		if len(t) > leaf {
			return truncateMiddle(t, leaf, fieldOmissionMarker)
		}
		return t
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[k] = capStrings(x, leaf)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, x := range t {
			out[i] = capStrings(x, leaf)
		}
		return out
	}
	return v
}

func textOmissionMarker(omittedBytes, omittedLines, totalBytes int) string {
	return fmt.Sprintf("\n\n[... %d bytes (%d lines) omitted from the middle of this tool result (%d bytes in all) to fit the context. "+
		"The omitted part is not in the conversation. To see it, call the tool again asking for less: "+
		"a narrower range (offset/limit), a search pattern, or head/tail of the output.]\n\n",
		omittedBytes, omittedLines, totalBytes)
}

func fieldOmissionMarker(omittedBytes, omittedLines, totalBytes int) string {
	return fmt.Sprintf("\n[... %d bytes (%d lines) omitted from the middle of this field (%d bytes in all). "+
		"Call again asking for less (offset/limit, a search pattern, head/tail) to see it.]\n",
		omittedBytes, omittedLines, totalBytes)
}

// truncateMiddle keeps the head and the tail of s and replaces the middle
// with marker, so the result is at most about limit bytes. Cuts prefer line
// boundaries and never split a UTF-8 sequence.
func truncateMiddle(s string, limit int, marker func(omittedBytes, omittedLines, totalBytes int) string) string {
	if len(s) <= limit {
		return s
	}
	// Size the marker for the worst case (everything omitted) so the real
	// one, with smaller numbers, can only be shorter.
	budget := limit - len(marker(len(s), strings.Count(s, "\n")+1, len(s)))
	if budget < 2 {
		budget = 2
	}
	headLen := budget / 2
	tailLen := budget - headLen

	head := s[:headLen]
	for len(head) > 0 && !utf8.RuneStart(s[len(head)]) {
		head = head[:len(head)-1]
	}
	if nl := strings.LastIndexByte(head, '\n'); nl >= 0 && nl >= len(head)*3/4 {
		head = head[:nl+1]
	}

	start := len(s) - tailLen
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	tail := s[start:]
	if nl := strings.IndexByte(tail, '\n'); nl >= 0 && nl < len(tail)/4 {
		tail = tail[nl+1:]
	}

	omitted := s[len(head) : len(s)-len(tail)]
	return head + marker(len(omitted), strings.Count(omitted, "\n"), len(s)) + tail
}

// reportToolOutputTruncations makes every cut visible: a log line, and the
// observers that asked. The model already sees the marker in the result.
func (r *Runtime) reportToolOutputTruncations(ctx context.Context, cuts []ToolOutputTruncation) {
	if r == nil || r.svc == nil || len(cuts) == 0 {
		return
	}
	s := r.svc
	s.observersMu.RLock()
	snapshot := make([]Observer, len(s.observers))
	copy(snapshot, s.observers)
	s.observersMu.RUnlock()
	for _, cut := range cuts {
		cut.TaskID = currentTaskID(r.session)
		cut.RunID = r.runID()
		cut.SessionID = r.sessionID()
		cut.AgentName = r.currentAgentName()
		cut.Round = r.currentRound
		r.log().Info("tool output truncated for the model",
			slog.String("tool", cut.ToolName),
			slog.String("tool_call_id", cut.ToolCallID),
			slog.Int("original_bytes", cut.OriginalBytes),
			slog.Int("kept_bytes", cut.KeptBytes),
			slog.Int("limit", cut.Limit),
			slog.Bool("structure_kept", cut.StructureKept))
		for _, o := range snapshot {
			to, ok := o.(ToolOutputObserver)
			if !ok {
				continue
			}
			c := cut
			s.invokeObserver(o, func(Observer) { to.OnToolOutputTruncated(ctx, c) })
		}
	}
}
