package agent

import "testing"

func TestNoRawPTCCodeLint(t *testing.T) {
	lint := NoRawPTCCode()
	leak := "<code>const r = callTool('list_schedules', {}); return toolData(r);</code>"
	if ok, reason := lint.Check(leak, LintContext{}); ok || reason == "" {
		t.Fatalf("expected leaked PTC code to be rejected, got ok=%v reason=%q", ok, reason)
	}
	clean := "你这周五下午三点约了老王，在楼下星巴克。"
	if ok, _ := lint.Check(clean, LintContext{}); !ok {
		t.Fatal("clean answer should pass")
	}
	// A prose mention of tools (no sandbox call syntax) must not false-positive.
	mention := "我调用了 list_schedules 工具来查日程。"
	if ok, _ := lint.Check(mention, LintContext{}); !ok {
		t.Fatal("plain mention of a tool name should pass")
	}
}
