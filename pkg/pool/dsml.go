package pool

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// DeepSeek's own tool-call markup, leaking into the answer.
//
// Now and then deepseek-flash answers a request that offered tools with its
// native call syntax written out as text instead of a tool_calls array:
//
//	<｜｜DSML｜｜ calls>
//	<｜｜DSML｜｜ invoke name="memory_recall">
//	<｜｜DSML｜｜ parameter name="query" string="true">CortexDB 工具名称</｜｜DSML｜｜ parameter>
//	</｜｜DSML｜｜ invoke>
//	</｜｜DSML｜｜ calls>
//
// The run took that for the final answer and showed it to the person (seen
// on a live turn: the reply was the markup, the tool never ran). Like the
// Bedrock EventStream case above, the calls are recovered from the text so
// the run goes on as if the provider had sent them properly. The bars are
// fullwidth (U+FF5C); how many there are varies, so the patterns only insist
// on "DSML" inside an angle-bracketed tag.

var (
	dsmlInvokeRe = regexp.MustCompile(`(?s)<[^<>]*DSML[^<>]*invoke\s+name="([^"]+)"[^<>]*>(.*?)</[^<>]*DSML[^<>]*invoke\s*>`)
	dsmlParamRe  = regexp.MustCompile(`(?s)<[^<>]*DSML[^<>]*parameter\s+name="([^"]+)"([^<>]*)>(.*?)</[^<>]*DSML[^<>]*parameter\s*>`)
	dsmlBlockRe  = regexp.MustCompile(`(?s)<[^<>]*DSML[^<>]*calls[^<>]*>.*?</[^<>]*DSML[^<>]*calls\s*>`)
)

// dsmlStart is how every DeepSeek special token begins.
const dsmlStart = "<｜"

// extractDSMLToolCalls recovers tool calls written as DSML text. It returns
// the calls and the text with the markup removed; no calls means the text
// was not DSML and comes back unchanged.
func extractDSMLToolCalls(text string) ([]domain.ToolCall, string) {
	if !strings.Contains(text, "DSML") {
		return nil, text
	}
	var calls []domain.ToolCall
	for i, m := range dsmlInvokeRe.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		args := map[string]interface{}{}
		for _, p := range dsmlParamRe.FindAllStringSubmatch(m[2], -1) {
			key, attrs, raw := strings.TrimSpace(p[1]), p[2], p[3]
			if key == "" {
				continue
			}
			// string="true" says the value is text as written; otherwise it
			// is JSON (a number, a list, an object) when it parses as one.
			var v interface{} = raw
			if !strings.Contains(attrs, `string="true"`) {
				var decoded interface{}
				if json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded) == nil {
					v = decoded
				}
			}
			args[key] = v
		}
		calls = append(calls, domain.ToolCall{
			ID:       fmt.Sprintf("dsml_%d", i),
			Type:     "function",
			Function: domain.FunctionCall{Name: name, Arguments: args},
		})
	}
	if len(calls) == 0 {
		return nil, text
	}
	cleaned := dsmlBlockRe.ReplaceAllString(text, "")
	cleaned = dsmlInvokeRe.ReplaceAllString(cleaned, "")
	return calls, strings.TrimSpace(cleaned)
}

// dsmlHold keeps DSML markup out of a stream's visible text. From the first
// "<｜" on, the text is held rather than shown; at the end it is either
// recovered as tool calls or, when it was not DSML after all, released.
type dsmlHold struct {
	holding bool
	held    strings.Builder
	tail    string // a trailing "<" that may be the start of "<｜"
}

func (h *dsmlHold) feed(text string) string {
	if text == "" {
		return ""
	}
	if h.holding {
		h.held.WriteString(text)
		return ""
	}
	s := h.tail + text
	h.tail = ""
	if i := strings.Index(s, dsmlStart); i >= 0 {
		h.holding = true
		h.held.WriteString(s[i:])
		return s[:i]
	}
	if strings.HasSuffix(s, "<") {
		h.tail = "<"
		return s[:len(s)-1]
	}
	return s
}

// release returns everything held, as written.
func (h *dsmlHold) release() string {
	out := h.tail + h.held.String()
	h.tail, h.holding = "", false
	h.held.Reset()
	return out
}
