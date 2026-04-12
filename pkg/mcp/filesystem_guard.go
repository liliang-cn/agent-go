package mcp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

var defaultFilesystemIgnoreNames = []string{
	".agentgo",
	".git",
	".hg",
	".svn",
	".snapshots",
	".next",
	".nuxt",
	".turbo",
	".cache",
	".venv",
	"__pycache__",
	"node_modules",
	"vendor",
	"target",
	"dist",
	"build",
	"bin",
	"coverage",
	"tmp",
	"temp",
	"out",
}

type FilesystemPathBlockedError struct {
	Path string
}

func (e FilesystemPathBlockedError) Error() string {
	return fmt.Sprintf("filesystem path is blocked by blacklist: %s", e.Path)
}

func (e FilesystemPathBlockedError) BlockedReason() string {
	return e.Error()
}

func DefaultFilesystemIgnoreNames() []string {
	return append([]string(nil), defaultFilesystemIgnoreNames...)
}

func normalizeFilesystemIgnoreNames(names []string) []string {
	if len(names) == 0 {
		return DefaultFilesystemIgnoreNames()
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return DefaultFilesystemIgnoreNames()
	}
	return out
}

func isFilesystemTool(toolName string) bool {
	return strings.HasPrefix(toolName, "mcp_filesystem_")
}

func isBlacklistedFilesystemPath(path string, ignoreNames []string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return false
	}
	if isAgentGoWorkspacePath(cleaned) {
		return false
	}
	ignoreNames = normalizeFilesystemIgnoreNames(ignoreNames)
	parts := strings.Split(filepath.ToSlash(cleaned), "/")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if slices.Contains(ignoreNames, part) {
			return true
		}
	}
	return false
}

func isAgentGoWorkspacePath(path string) bool {
	normalized := filepath.ToSlash(filepath.Clean(path))
	return strings.Contains(normalized, "/.agentgo/workspace/") || strings.HasSuffix(normalized, "/.agentgo/workspace")
}

func validateFilesystemToolArgs(toolName string, arguments map[string]interface{}, ignoreNames []string) error {
	if !isFilesystemTool(toolName) || toolName == "mcp_filesystem_list_allowed_directories" {
		return nil
	}

	var paths []string
	collectPathArg := func(key string) {
		if val, ok := arguments[key]; ok {
			if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
				paths = append(paths, s)
			}
		}
	}

	collectPathArg("path")
	collectPathArg("source")
	collectPathArg("destination")
	if raw, ok := arguments["paths"].([]interface{}); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				paths = append(paths, s)
			}
		}
	}

	for _, p := range paths {
		if isBlacklistedFilesystemPath(p, ignoreNames) {
			return FilesystemPathBlockedError{Path: p}
		}
	}
	return nil
}

func sanitizeFilesystemToolArgs(toolName string, arguments map[string]interface{}) map[string]interface{} {
	if len(arguments) == 0 || !isFilesystemTool(toolName) {
		return arguments
	}

	arguments = normalizeFilesystemPathAliases(arguments)
	switch toolName {
	case "mcp_filesystem_write_file":
		return sanitizeFilesystemWriteArgs(arguments)
	case "mcp_filesystem_modify_file":
		return sanitizeFilesystemModifyArgs(arguments)
	default:
		return arguments
	}
}

func normalizeFilesystemPathAliases(arguments map[string]interface{}) map[string]interface{} {
	if _, ok := arguments["path"].(string); ok {
		return arguments
	}
	for _, key := range []string{"file_path", "filepath", "filePath", "filename", "file_name", "fileName", "file", "name", "target_path", "targetPath", "target", "uri"} {
		if value, ok := arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			cloned := cloneToolArgs(arguments)
			cloned["path"] = strings.TrimSpace(value)
			return cloned
		}
	}
	return arguments
}

func sanitizeFilesystemWriteArgs(arguments map[string]interface{}) map[string]interface{} {
	content, ok := arguments["content"].(string)
	if !ok {
		return arguments
	}
	sanitized := stripUnsupportedControlChars(content)
	if sanitized == content {
		return arguments
	}
	cloned := cloneToolArgs(arguments)
	cloned["content"] = sanitized
	return cloned
}

func sanitizeFilesystemModifyArgs(arguments map[string]interface{}) map[string]interface{} {
	replace, ok := arguments["replace"].(string)
	if !ok {
		return arguments
	}
	sanitized := stripUnsupportedControlChars(replace)
	if sanitized == replace {
		return arguments
	}
	cloned := cloneToolArgs(arguments)
	cloned["replace"] = sanitized
	return cloned
}

func cloneToolArgs(arguments map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(arguments))
	for key, value := range arguments {
		cloned[key] = value
	}
	return cloned
}

func stripUnsupportedControlChars(input string) string {
	if input == "" {
		return input
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, input)
}

func filterFilesystemToolResult(toolName string, result *ToolResult, ignoreNames []string) *ToolResult {
	if result == nil || !result.Success || !isFilesystemTool(toolName) {
		return result
	}

	text, ok := result.Data.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return result
	}

	switch toolName {
	case "mcp_filesystem_list_directory", "mcp_filesystem_search_files", "mcp_filesystem_search_within_files", "mcp_filesystem_tree":
		lines := strings.Split(text, "\n")
		filtered := make([]string, 0, len(lines))
		for _, line := range lines {
			if isBlacklistedFilesystemPath(extractFilesystemPathFromLine(line), ignoreNames) {
				continue
			}
			filtered = append(filtered, line)
		}
		cloned := *result
		cloned.Data = strings.Join(filtered, "\n")
		return &cloned
	default:
		return result
	}
}

func extractFilesystemPathFromLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if start := strings.Index(line, "file://"); start >= 0 {
		path := line[start+len("file://"):]
		if end := strings.Index(path, ")"); end >= 0 {
			path = path[:end]
		}
		return path
	}
	return line
}
