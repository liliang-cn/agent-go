package agent

import (
	"testing"
)

func TestSanitiseJSCode_Clean(t *testing.T) {
	// Clean JS should pass through unchanged
	code := `const data = callTool('get_team_members', { department: 'engineering' });
return data.members;`
	got := sanitiseJSCode(code)
	if got != code {
		t.Errorf("clean JS was modified:\ngot:  %q\nwant: %q", got, code)
	}
}

func TestSanitiseJSCode_MarkdownFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "javascript fence",
			input: "```javascript\nconst x = 1;\nreturn x;\n```",
			want:  "const x = 1;\nreturn x;",
		},
		{
			name:  "js fence",
			input: "```js\ncallTool('foo', {});\n```",
			want:  "callTool('foo', {});",
		},
		{
			name:  "bare fence",
			input: "```\nconst y = 2;\n```",
			want:  "const y = 2;",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitiseJSCode(tt.input)
			if got != tt.want {
				t.Errorf("got:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestSanitiseJSCode_TrailingProse(t *testing.T) {
	code := `const data = callTool('get_team_members', { department: 'engineering' });
const result = data.members.map(m => m.name);
return result;
It seems that I encountered an issue with accessing the data tools.
If you have any other questions, feel free to ask!`

	got := sanitiseJSCode(code)
	want := `const data = callTool('get_team_members', { department: 'engineering' });
const result = data.members.map(m => m.name);
return result;`
	if got != want {
		t.Errorf("trailing prose not stripped:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSanitiseJSCode_TrailingJSON(t *testing.T) {
	code := `const data = callTool('get_team_members', { department: 'engineering' });
return data;
{
  "queries": [
    "engineering team members"
  ]
}`
	got := sanitiseJSCode(code)
	want := `const data = callTool('get_team_members', { department: 'engineering' });
return data;`
	if got != want {
		t.Errorf("trailing JSON not stripped:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSanitiseJSCode_MixedGarbage(t *testing.T) {
	// Reproduces the actual failure from gpt-5.2: JS code followed by JSON + prose
	code := `// Step 1: Retrieve team members from the engineering department.
const members = callTool('get_team_members', { department: 'engineering' });
return members;
{
  "queries": [
    "engineering team members who exceeded their Q3 travel budget allocation"
  ]
}It seems that I encountered an issue with accessing the data tools required for the query. Unfortunately, I can't proceed with the requested task at the moment.

If you have any other questions or need assistance with something else, feel free to ask!`

	got := sanitiseJSCode(code)
	want := `// Step 1: Retrieve team members from the engineering department.
const members = callTool('get_team_members', { department: 'engineering' });
return members;`
	if got != want {
		t.Errorf("mixed garbage not stripped:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSanitiseJSCode_TrailingPythonBlock(t *testing.T) {
	// gpt-5.2 appends a Python code block after the JS return statement
	code := "// Get team members\r\nconst data = callTool('get_team_members', { department: 'engineering' });\r\nreturn data;\r\n```python\n# Fallback code\nimport pandas as pd\ndf = pd.DataFrame([])\ndf\n```\nI have processed the data."

	got := sanitiseJSCode(code)
	want := "// Get team members\nconst data = callTool('get_team_members', { department: 'engineering' });\nreturn data;"
	if got != want {
		t.Errorf("trailing python block not stripped:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSanitiseJSCode_CRLFLineEndings(t *testing.T) {
	// Verify \r\n normalisation
	code := "const x = 1;\r\nreturn x;\r\n"
	got := sanitiseJSCode(code)
	want := "const x = 1;\nreturn x;"
	if got != want {
		t.Errorf("CRLF not normalised:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSanitiseJSCode_NoTrailingNewlineDoesNotPanic(t *testing.T) {
	code := `const req = "write file";
const r = callTool('route_builtin_request', { request: req });
console.log(r);
return r;`
	got := sanitiseJSCode(code)
	if got != code {
		t.Errorf("clean JS without trailing newline was modified:\ngot:  %q\nwant: %q", got, code)
	}
}

func TestIsNaturalLanguageLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"It seems there was an error", true},
		{"I encountered an issue", true},
		{"Unfortunately, the tool failed", true},
		{"const x = 1;", false},
		{"return result;", false},
		{"", false},
		{"// This is a comment", false},
		{"Please try again", true},
		{"```python", true},
		{"```", true},
	}
	for _, tt := range tests {
		got := isNaturalLanguageLine(tt.line)
		if got != tt.want {
			t.Errorf("isNaturalLanguageLine(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestSanitiseJSCode_SameLineSemicolonJSON(t *testing.T) {
	// Reproduces the case where gpt-5.2 puts JSON garbage on the same line
	// as the return statement's semicolon: `return result;{"queries": [...]}`
	code := `const members = callTool('get_team_members', { department: 'engineering' });
return members;{
  "queries": [
    "engineering team members"
  ]
}`
	got := sanitiseJSCode(code)
	want := `const members = callTool('get_team_members', { department: 'engineering' });
return members;`
	if got != want {
		t.Errorf("same-line ;{ garbage not stripped:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSanitiseJSCode_SameLineSemicolonProse(t *testing.T) {
	// Semicolon followed by prose on the same line
	code := `const data = callTool('get_expenses', { team: 'engineering' });
return data;I hope this helps! Let me know if you need anything else.`
	got := sanitiseJSCode(code)
	want := `const data = callTool('get_expenses', { team: 'engineering' });
return data;`
	if got != want {
		t.Errorf("same-line ; prose not stripped:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestExtractBetweenTags(t *testing.T) {
	tests := []struct {
		name    string
		content string
		start   string
		end     string
		want    string
	}{
		{
			name:    "basic code tags",
			content: "<code>\nconst x = 1;\nreturn x;\n</code>",
			start:   "<code>",
			end:     "</code>",
			want:    "const x = 1;\nreturn x;",
		},
		{
			name:    "code tags with garbage after",
			content: "<code>\nreturn 42;\n</code>\nIt seems there was an issue...",
			start:   "<code>",
			end:     "</code>",
			want:    "return 42;",
		},
		{
			name:    "code tags with garbage before",
			content: "Sure! Here is the code:\n<code>\nreturn 42;\n</code>",
			start:   "<code>",
			end:     "</code>",
			want:    "return 42;",
		},
		{
			name:    "no tags",
			content: "const x = 1;\nreturn x;",
			start:   "<code>",
			end:     "</code>",
			want:    "",
		},
		{
			name:    "missing end tag",
			content: "<code>\nconst x = 1;",
			start:   "<code>",
			end:     "</code>",
			want:    "",
		},
		{
			name:    "empty code block",
			content: "<code>\n</code>",
			start:   "<code>",
			end:     "</code>",
			want:    "",
		},
		{
			name: "complex JS in code tags",
			content: `<code>
const data = callTool('get_team_members', { department: 'engineering' });
const results = data.members.map(m => {
  const exp = callTool('get_expenses', { member_id: m.id });
  return { name: m.name, total: exp.expenses.reduce((s, e) => s + e.amount, 0) };
});
return results;
</code>`,
			start: "<code>",
			end:   "</code>",
			want: `const data = callTool('get_team_members', { department: 'engineering' });
const results = data.members.map(m => {
  const exp = callTool('get_expenses', { member_id: m.id });
  return { name: m.name, total: exp.expenses.reduce((s, e) => s + e.amount, 0) };
});
return results;`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBetweenTags(tt.content, tt.start, tt.end)
			if got != tt.want {
				t.Errorf("extractBetweenTags():\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestExtractCode_IgnoresThinkBlocksBeforeCode(t *testing.T) {
	ptc, err := NewPTCIntegration(DefaultPTCConfig(), nil)
	if err != nil {
		t.Fatalf("NewPTCIntegration() error = %v", err)
	}

	content := `<think>
I should inspect the workspace and maybe call several tools first.
</think>
<code>
const content = "# Test Search\n说明\n完成时间";
callTool('mcp_filesystem_write_file', {
  path: 'workspace/test_search.md',
  content,
});
return 'workspace/test_search.md';
</code>`

	if !ptc.IsCodeResponse(content) {
		t.Fatalf("IsCodeResponse() = false, want true")
	}

	got := ptc.ExtractCode(content)
	want := `const content = "# Test Search\n说明\n完成时间";
callTool('mcp_filesystem_write_file', {
  path: 'workspace/test_search.md',
  content,
});
return 'workspace/test_search.md';`
	if got != want {
		t.Fatalf("ExtractCode() mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestLooksLikeCompleteJS(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"return data;", true},
		{"const x = {}", true},
		{"callTool('foo', {})", true},
		{"[1, 2, 3]", true},
		{"some random text", false},
		{"", false},
	}
	for _, tt := range tests {
		got := looksLikeCompleteJS(tt.code)
		if got != tt.want {
			t.Errorf("looksLikeCompleteJS(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}
