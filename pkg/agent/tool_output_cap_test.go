package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func numberedText(n int, line string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%05d %s\n", i, line)
	}
	return b.String()
}

func TestTruncateMiddleKeepsHeadAndTail(t *testing.T) {
	s := numberedText(5000, "some output line")
	out := truncateMiddle(s, 4000, textOmissionMarker)
	if len(out) > 4000 {
		t.Fatalf("cut result is %d bytes, over 4000", len(out))
	}
	if !strings.HasPrefix(out, "00001 ") || !strings.HasSuffix(out, "05000 some output line\n") {
		t.Fatalf("head or tail lost:\n%s", out)
	}
	if !strings.Contains(out, "omitted from the middle") || !strings.Contains(out, "offset/limit") {
		t.Fatal("the marker must say what was cut and how to get it")
	}
	// Byte-stable: the same result always renders the same bytes, so a
	// message written into history never changes the prompt prefix.
	if again := truncateMiddle(s, 4000, textOmissionMarker); again != out {
		t.Fatal("truncation is not deterministic")
	}
}

func TestTruncateMiddleNeverSplitsUTF8(t *testing.T) {
	s := strings.Repeat("工具输出统一上限", 3000) // no newlines, multi-byte runes
	out := truncateMiddle(s, 1000, textOmissionMarker)
	if !utf8.ValidString(out) {
		t.Fatal("cut split a UTF-8 sequence")
	}
	if len(out) > 1000 {
		t.Fatalf("%d bytes over 1000", len(out))
	}
}

// An MCP tool returning a huge plain string is capped exactly like a built-in.
func TestCapToolResultPlainString(t *testing.T) {
	s := numberedText(20000, "mcp row")
	out, info, ok := capToolResult(s, s, DefaultToolOutputLimit)
	if !ok || len(out) > DefaultToolOutputLimit || info.StructureKept {
		t.Fatalf("ok=%v len=%d info=%+v", ok, len(out), info)
	}
	if info.OriginalBytes != len(s) || info.KeptBytes != len(out) {
		t.Fatalf("sizes misreported: %+v", info)
	}
	if _, _, cut := capToolResult("small", "small", DefaultToolOutputLimit); cut {
		t.Fatal("a small result must pass through untouched")
	}
}

// A structured result whose bulk is not in one string (a huge array of small
// objects) cannot be cut inside fields; it is cut as text instead of being
// sent whole.
func TestCapToolResultArrayFallsBackToText(t *testing.T) {
	rows := make([]map[string]interface{}, 5000)
	for i := range rows {
		rows[i] = map[string]interface{}{"id": i, "name": "row"}
	}
	text := toolResultToString(rows)
	out, info, ok := capToolResult(rows, text, 2000)
	if !ok || info.StructureKept || len(out) > 2000 {
		t.Fatalf("ok=%v kept=%v len=%d", ok, info.StructureKept, len(out))
	}
}

// The cap rewrites content only: every tool message still answers the call id
// it answered before, and a small result is byte-identical.
func TestAppendToolRoundKeepsCallIDPairing(t *testing.T) {
	svc := &Service{toolOutputLimit: 3000}
	big := numberedText(4000, "x")
	result := &domain.GenerationResult{ToolCalls: []domain.ToolCall{
		{ID: "a", Type: "function", Function: domain.FunctionCall{Name: "big"}},
		{ID: "b", Type: "function", Function: domain.FunctionCall{Name: "small"}},
	}}
	msgs, cuts := svc.appendToolRoundToMessages(nil, "", result, []ToolExecutionResult{
		{ToolCallID: "a", ToolName: "big", Result: big},
		{ToolCallID: "b", ToolName: "small", Result: map[string]interface{}{"ok": true}},
	})
	if len(msgs) != 3 || msgs[1].ToolCallID != "a" || msgs[2].ToolCallID != "b" {
		t.Fatalf("pairing broken: %+v", msgs)
	}
	if len(msgs[1].Content) > 3000 {
		t.Fatalf("big result not capped: %d", len(msgs[1].Content))
	}
	if msgs[2].Content != `{"ok":true}` {
		t.Fatalf("small result changed: %q", msgs[2].Content)
	}
	if len(cuts) != 1 || cuts[0].ToolCallID != "a" || cuts[0].ToolName != "big" {
		t.Fatalf("cuts = %+v", cuts)
	}
}

func TestToolOutputCapResolution(t *testing.T) {
	cases := []struct{ set, want int }{{0, DefaultToolOutputLimit}, {-1, 0}, {5000, 5000}}
	for _, c := range cases {
		if got := (&Service{toolOutputLimit: c.set}).toolOutputCap(); got != c.want {
			t.Errorf("limit %d resolved to %d, want %d", c.set, got, c.want)
		}
	}
}

// Following next_offset from a default read walks the whole file, once.
func TestFsReadPagesThroughWholeFile(t *testing.T) {
	text := numberedText(7000, strings.Repeat("y", 60))
	offset, seen, pages := 0, 0, 0
	for {
		p := withLineNumbers(text, offset, 0)
		pages++
		if len(p.Text) > fsReadPageBytes+200 {
			t.Fatalf("page %d is %d bytes", pages, len(p.Text))
		}
		if p.TotalLines != 7000 || p.StartLine != offset+1 {
			t.Fatalf("page %d: %+v", pages, p)
		}
		seen += p.EndLine - p.StartLine + 1
		if p.NextOffset < 0 {
			break
		}
		if !strings.Contains(p.Text, fmt.Sprintf("offset=%d", p.NextOffset)) {
			t.Fatal("the page text must name the next offset")
		}
		offset = p.NextOffset
	}
	if seen != 7000 {
		t.Fatalf("paging covered %d of 7000 lines in %d pages", seen, pages)
	}
	// An explicit limit is honoured as given.
	if p := withLineNumbers(text, 0, 5000); p.EndLine != 5000 || p.NextOffset != 5000 {
		t.Fatalf("explicit limit: start=%d end=%d next=%d", p.StartLine, p.EndLine, p.NextOffset)
	}
	// A small file reads whole with no trailer.
	if p := withLineNumbers("a\nb\n", 0, 0); p.NextOffset != -1 || strings.Contains(p.Text, "continue") {
		t.Fatalf("small file: %+v", p)
	}
}

// One enormous line still pages: it is cut, not returned whole.
func TestFsReadCutsGiantLine(t *testing.T) {
	p := withLineNumbers(strings.Repeat("z", 1_000_000), 0, 0)
	if len(p.Text) > fsReadMaxLineChars+200 || !strings.Contains(p.Text, "line truncated") {
		t.Fatalf("giant line rendered as %d bytes", len(p.Text))
	}
}

func TestStructuredCapStaysValidJSON(t *testing.T) {
	res := toolOK(map[string]interface{}{"path": "a", "content": numberedText(9000, "<b>&</b>"), "total_lines": 9000})
	text := toolResultToString(res)
	out, info, ok := capToolResult(res, text, 8000)
	if !ok || !info.StructureKept || len(out) > 8000 {
		t.Fatalf("ok=%v kept=%v len=%d", ok, info.StructureKept, len(out))
	}
	if !json.Valid([]byte(out)) {
		t.Fatal("not valid JSON")
	}
}
