package agent

import (
	"context"
	"testing"
)

func TestBuildWebSearchBody(t *testing.T) {
	b := buildWebSearchBody("qwen-plus", "今天 SpaceX 新闻")
	if b["enable_search"] != true {
		t.Fatalf("expected enable_search=true")
	}
	if _, ok := b["web_search_options"]; !ok {
		t.Fatalf("expected web_search_options present")
	}
	if b["model"] != "qwen-plus" {
		t.Fatalf("model not set")
	}
	msgs, ok := b["messages"].([]map[string]string)
	if !ok || len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("bad messages: %#v", b["messages"])
	}
}

func TestWebSearchNotConfigured(t *testing.T) {
	if _, err := WebSearch(context.Background(), WebSearchConfig{}, "q"); err == nil {
		t.Fatal("expected error when not configured")
	}
}
