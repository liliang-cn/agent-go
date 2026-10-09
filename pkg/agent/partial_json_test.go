package agent

import (
	"encoding/json"
	"testing"
)

func TestPartialJSONStringField(t *testing.T) {
	cases := []struct{ raw, want string }{
		{``, ""},
		{`{`, ""},
		{`{"res`, ""},
		{`{"result"`, ""},
		{`{"result": `, ""},
		{`{"result": "`, ""},
		{`{"result": "Disk on orange2 is 9`, "Disk on orange2 is 9"},
		{`{"result": "line one\nline`, "line one\nline"},
		{`{"result": "a \"quoted\" wo`, `a "quoted" wo`},
		{`{"result": "trailing \`, "trailing "},
		{`{"result": "half \u78`, "half "},
		{`{"result": "磁盘 磁盘 ok"}`, "磁盘 磁盘 ok"},
		{`{"result": "face 😀!"}`, "face 😀!"},
		{`{"result": "face \ud83d`, "face "},
		{`{"other": {"x": [1, "a,b"]}, "result": "after`, "after"},
		{`{"other": "x", "result": "done"}`, "done"},
		{`{"result": 42}`, ""},
	}
	for _, c := range cases {
		if got := partialJSONStringField(c.raw, "result"); got != c.want {
			t.Errorf("partialJSONStringField(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// Every prefix of a real arguments string decodes to a prefix of the final
// value, so streamed pieces join up to exactly the answer.
func TestPartialJSONStringFieldPrefixesJoinUp(t *testing.T) {
	answer := "## 报告\n\n| 机器 | 状态 |\n|---|---|\n| orange2 | \"满\" \\ 93% 😀 |\n"
	raw, _ := json.Marshal(map[string]string{"result": answer})
	sent := ""
	for i := 0; i <= len(raw); i++ {
		got := partialJSONStringField(string(raw[:i]), "result")
		if len(got) < len(sent) || got[:len(sent)] != sent {
			t.Fatalf("prefix %d went back: had %q, now %q", i, sent, got)
		}
		sent = got
	}
	if sent != answer {
		t.Fatalf("joined = %q, want %q", sent, answer)
	}
}
