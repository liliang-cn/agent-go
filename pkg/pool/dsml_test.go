package pool

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reply a live deepseek-flash turn gave instead of a tool call.
const leakedDSML = "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"memory_recall\">\n<｜｜DSML｜｜ parameter name=\"query\" string=\"true\">CortexDB 工具名称</｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"limit\">5</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"

func TestExtractDSMLToolCalls(t *testing.T) {
	calls, rest := extractDSMLToolCalls(leakedDSML)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	c := calls[0]
	if c.Function.Name != "memory_recall" || c.Function.Arguments["query"] != "CortexDB 工具名称" {
		t.Fatalf("call = %+v", c)
	}
	if n, ok := c.Function.Arguments["limit"].(float64); !ok || n != 5 {
		t.Fatalf("a parameter without string=\"true\" should be read as JSON: %#v", c.Function.Arguments["limit"])
	}
	if rest != "" {
		t.Fatalf("markup left in the answer: %q", rest)
	}

	plain := "用 a<b 比较，结果是 <｜ 不是标记"
	if calls, rest := extractDSMLToolCalls(plain); calls != nil || rest != plain {
		t.Fatalf("plain text was changed: %v %q", calls, rest)
	}
}

// streamText runs text through a stream state in chunks of n bytes' worth of
// runes, and returns what the reader saw and the final result.
func streamText(t *testing.T, text string, n int) (seen string, final string, tools []string) {
	t.Helper()
	var st poolStreamState
	runes := []rune(text)
	for i := 0; i < len(runes); i += n {
		j := i + n
		if j > len(runes) {
			j = len(runes)
		}
		var ch poolStreamChunk
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": string(runes[i:j])}}}})
		if err := json.Unmarshal(raw, &ch); err != nil {
			t.Fatal(err)
		}
		if r, _ := st.absorb(ch); r != nil {
			seen += r.Content
		}
	}
	out := st.finish()
	for _, c := range out.ToolCalls {
		tools = append(tools, c.Function.Name)
	}
	return seen, out.Content, tools
}

// Streamed, the markup never reaches the reader — however the chunks cut it —
// and the calls come out at the end.
func TestStreamHoldsBackDSML(t *testing.T) {
	for _, n := range []int{1, 2, 7, 1000} {
		seen, final, tools := streamText(t, "好的，我查一下。\n"+leakedDSML, n)
		if strings.Contains(seen+final, "DSML") || strings.Contains(seen, "<｜") {
			t.Fatalf("chunk %d: markup shown: seen=%q final=%q", n, seen, final)
		}
		if !strings.Contains(seen+final, "好的，我查一下。") {
			t.Fatalf("chunk %d: the words before the markup were lost: %q %q", n, seen, final)
		}
		if len(tools) != 1 || tools[0] != "memory_recall" {
			t.Fatalf("chunk %d: tools = %v", n, tools)
		}
	}
}

// Text that only looks like the start of a special token is still all said.
func TestStreamReleasesWhatWasNotDSML(t *testing.T) {
	text := "比较 a<b，再看 <｜ 这个符号。"
	for _, n := range []int{1, 3, 1000} {
		seen, final, tools := streamText(t, text, n)
		if seen+final != text || len(tools) != 0 {
			t.Fatalf("chunk %d: got %q + %q, tools %v", n, seen, final, tools)
		}
	}
}

func TestParsePoolToolResponseRecoversDSML(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": leakedDSML}, "finish_reason": "stop"}}})
	out, err := parsePoolToolResponse(raw, "deepseek-flash")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Function.Name != "memory_recall" || out.Content != "" {
		t.Fatalf("got %+v", out)
	}
}
