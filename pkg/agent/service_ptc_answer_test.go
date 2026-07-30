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
		// PTC's call protocol. A scheduled run handed this whole transcript over
		// as its answer — the script, then the tool's JSON — because none of the
		// markers above appear in it.
		"<code>\nconst res = callTool('mcp_websearch_websearch', {query: '上证指数'});\nreturn toolData(res);\n</code>",
		"我查一下。\n<code>\nreturn callTool('search_available_tools', {query: 'websearch'});\n</code>\n{\"results\":[]}",
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
		// Markdown fences are how a real reply shows code; only PTC's <code> tags
		// mean "run this".
		"用这段就行：\n```js\nconst a = 1;\n```\n情绪: 中性",
	}
	for _, text := range answers {
		if looksLikeExecutionReport(text) {
			t.Errorf("should be kept as the answer: %q", text)
		}
	}
}

// TestStripPTCCodeBlocks pins the cleanup applied to the summarising round's
// reply. That round is given the agent's own system prompt, which teaches PTC, so
// it answers in PTC form: the sentence wrapped in a <code>return "…"</code> and
// then the sentence. A scheduled run delivered both.
func TestStripPTCCodeBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "code block then the answer",
			in:   "<code>\nreturn \"上证指数报 3865.23 点。\\n\\n情绪: 开心\";\n</code>上证指数报 3865.23 点。\n\n情绪: 开心",
			want: "上证指数报 3865.23 点。\n\n情绪: 开心",
		},
		{
			name: "nothing but a code block",
			in:   "<code>\nreturn \"done\";\n</code>",
			want: "",
		},
		{
			name: "two blocks around prose",
			in:   "<code>a</code>结果：好\n<code>b</code>",
			want: "结果：好",
		},
		{
			name: "a plain answer is untouched",
			in:   "上证指数报 3865.23 点。\n\n情绪: 开心",
			want: "上证指数报 3865.23 点。\n\n情绪: 开心",
		},
		{
			name: "markdown fences are not PTC and must survive",
			in:   "用这段：\n```js\nconst a = 1;\n```",
			want: "用这段：\n```js\nconst a = 1;\n```",
		},
	}
	for _, tc := range cases {
		if got := stripPTCCodeBlocks(tc.in); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
