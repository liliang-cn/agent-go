package domain

import (
	"strings"
	"testing"
)

func TestAnthropicIsADeclarableFormat(t *testing.T) {
	f, err := ParseNativeWebSearchFormat(" Anthropic ")
	if err != nil || f != NativeWebSearchAnthropic {
		t.Fatalf("parse = %q, %v", f, err)
	}
	if _, err := ParseNativeWebSearchFormat("claude"); err == nil || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("unknown format error = %v, want it to list anthropic", err)
	}
	if err := ValidateNativeWebSearchOptions(NativeWebSearchAnthropic, map[string]interface{}{"max_uses": 3}); err != nil {
		t.Fatalf("options refused: %v", err)
	}
}

func TestAnthropicWebSearchTool(t *testing.T) {
	tool := AnthropicWebSearchTool(nil)
	if tool["type"] != AnthropicWebSearchFiltering || tool["name"] != "web_search" || len(tool) != 2 {
		t.Fatalf("default tool = %v", tool)
	}
	tool = AnthropicWebSearchTool(map[string]interface{}{
		"type": AnthropicWebSearchBasic, "name": "search", "max_uses": 5,
		"allowed_domains": []string{"example.org"},
	})
	if tool["type"] != AnthropicWebSearchBasic || tool["name"] != "web_search" || tool["max_uses"] != 5 || tool["allowed_domains"] == nil {
		t.Fatalf("tool with options = %v", tool)
	}
}
