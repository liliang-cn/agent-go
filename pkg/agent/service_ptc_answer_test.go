package agent

import "testing"

// looksLikeExecutionReport decides whether the model already answered or whether
// its report has to be turned into one. Getting this wrong is exactly the bug it
// exists to fix: a false negative dumps raw JSON on the user, a false positive
// spends an extra round rewriting a perfectly good reply.
func TestLooksLikeExecutionReport(t *testing.T) {
	machineFacing := []string{
		"",
		"   \n ",
		"Code execution completed.\n**Status:** Success ✅\n{\"ok\":true}",
		"**Status:** Failed ❌\n**Error:** boom",
		"execute_javascript failed: timeout",
		"Direct tool-call fallback executed successfully.\n{}",
		`{"content":"tomorrow=2026-07-31","ok":true,"path":"dates.md"}`,
		`[{"a":1},{"b":2}]`,
	}
	for _, text := range machineFacing {
		if !looksLikeExecutionReport(text) {
			t.Errorf("should be treated as a report, not an answer: %q", text)
		}
	}

	answers := []string{
		"已写入工作区文件 `dates.md`：\n\ntomorrow=2026-07-31\n\n情绪: 开心",
		"明天是 2026-07-31，下周一是 2026-08-03。",
		"Done — the file is written.",
		// A sentence that merely mentions JSON is still a sentence.
		"我把结果按 JSON 存好了。",
		// Fenced code inside a real reply must not be mistaken for a payload.
		"结果如下：\n```json\n{\"ok\":true}\n```\n情绪: 中性",
	}
	for _, text := range answers {
		if looksLikeExecutionReport(text) {
			t.Errorf("should be kept as the answer: %q", text)
		}
	}
}
