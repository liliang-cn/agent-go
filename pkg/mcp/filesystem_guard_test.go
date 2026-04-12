package mcp

import (
	"strings"
	"testing"
)

func TestIsBlacklistedFilesystemPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "/repo/ui/node_modules/react/index.js", want: true},
		{path: "/repo/rust/target/debug/app", want: true},
		{path: "/repo/.git/config", want: true},
		{path: "/Users/alice/.agentgo/config/secrets.json", want: true},
		{path: "/Users/alice/.agentgo/workspace/note.md", want: false},
		{path: "/repo/ui/src/App.tsx", want: false},
	}

	for _, tc := range cases {
		if got := isBlacklistedFilesystemPath(tc.path, nil); got != tc.want {
			t.Fatalf("isBlacklistedFilesystemPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestValidateFilesystemToolArgs(t *testing.T) {
	err := validateFilesystemToolArgs("mcp_filesystem_read_file", map[string]interface{}{
		"path": "/repo/ui/node_modules/react/index.js",
	}, nil)
	if err == nil {
		t.Fatal("expected blacklisted path to be rejected")
	}

	err = validateFilesystemToolArgs("mcp_filesystem_read_file", map[string]interface{}{
		"path": "/repo/ui/src/App.tsx",
	}, nil)
	if err != nil {
		t.Fatalf("expected normal path to pass, got %v", err)
	}
}

func TestFilterFilesystemToolResult(t *testing.T) {
	result := &ToolResult{
		Success: true,
		Data: "Directory listing for: /repo/ui\n\n" +
			"[DIR]  node_modules (file:///repo/ui/node_modules)\n" +
			"[DIR]  src (file:///repo/ui/src)\n" +
			"[FILE] package.json (file:///repo/ui/package.json) - 100 bytes\n",
	}

	filtered := filterFilesystemToolResult("mcp_filesystem_list_directory", result, nil)
	text, _ := filtered.Data.(string)
	if text == "" {
		t.Fatal("expected filtered result text")
	}
	if wantGone := "node_modules"; strings.Contains(text, wantGone) {
		t.Fatalf("expected blacklisted entry %q to be removed: %s", wantGone, text)
	}
	if !strings.Contains(text, "src") {
		t.Fatalf("expected non-blacklisted entry to remain: %s", text)
	}
}

func TestSanitizeFilesystemToolArgs_StripsControlCharsFromWrites(t *testing.T) {
	args := map[string]interface{}{
		"path":    "/repo/workspace/note.md",
		"content": "hello\x02world\nnext\tline",
	}

	got := sanitizeFilesystemToolArgs("mcp_filesystem_write_file", args)
	content, _ := got["content"].(string)
	if strings.ContainsRune(content, '\x02') {
		t.Fatalf("expected control character to be stripped, got %q", content)
	}
	if content != "helloworld\nnext\tline" {
		t.Fatalf("unexpected sanitized content %q", content)
	}
}

func TestSanitizeFilesystemToolArgs_NormalizesPathAliases(t *testing.T) {
	args := map[string]interface{}{
		"file_path": "/repo/workspace/note.md",
		"content":   "hello",
	}

	got := sanitizeFilesystemToolArgs("mcp_filesystem_write_file", args)
	if got["path"] != "/repo/workspace/note.md" {
		t.Fatalf("expected path alias to be normalized, got %#v", got)
	}
	if _, ok := args["path"]; ok {
		t.Fatalf("expected original args to remain unchanged, got %#v", args)
	}
}

func TestSanitizeFilesystemToolArgs_ExistingPathWinsOverAlias(t *testing.T) {
	args := map[string]interface{}{
		"path":      "/repo/workspace/actual.md",
		"file_path": "/repo/workspace/alias.md",
	}

	got := sanitizeFilesystemToolArgs("mcp_filesystem_read_file", args)
	if got["path"] != "/repo/workspace/actual.md" {
		t.Fatalf("expected existing path to win, got %#v", got)
	}
}

func TestSanitizeFilesystemToolArgs_StripsControlCharsFromModifyReplace(t *testing.T) {
	args := map[string]interface{}{
		"path":    "/repo/workspace/note.md",
		"find":    "old",
		"replace": "new\x02value",
	}

	got := sanitizeFilesystemToolArgs("mcp_filesystem_modify_file", args)
	replace, _ := got["replace"].(string)
	if strings.ContainsRune(replace, '\x02') {
		t.Fatalf("expected control character to be stripped, got %q", replace)
	}
	if replace != "newvalue" {
		t.Fatalf("unexpected sanitized replace %q", replace)
	}
}
