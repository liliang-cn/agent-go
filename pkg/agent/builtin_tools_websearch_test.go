package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func TestBuildWebSearchBody(t *testing.T) {
	b := buildWebSearchBody(WebSearchConfig{Model: "qwen-plus"}, "今天 SpaceX 新闻")
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

func TestBuildWebSearchBodyDeclaredGoogle(t *testing.T) {
	b := buildWebSearchBody(WebSearchConfig{Model: "gemini", Native: domain.NativeWebSearchGoogle, ReasoningEffort: "low"}, "成都天气")
	if _, ok := b["enable_search"]; ok {
		t.Fatal("google_search declared, enable_search still sent")
	}
	if _, ok := b["web_search_options"]; ok {
		t.Fatal("google_search declared, web_search_options still sent")
	}
	tools, ok := b["tools"].([]map[string]any)
	if !ok || len(tools) != 1 || tools[0]["google_search"] == nil {
		t.Fatalf("tools: %#v", b["tools"])
	}
	if b["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort: %#v", b["reasoning_effort"])
	}
}

func TestWebSearchNotConfigured(t *testing.T) {
	if _, err := WebSearch(context.Background(), WebSearchConfig{}, "q"); err == nil {
		t.Fatal("expected error when not configured")
	}
}

// "anthropic" goes through the pool client: the request carries the
// web_search server tool and the answer's text comes back. The fake here
// speaks chat completions; the Messages translation is the pool's and is
// tested there.
func TestWebSearchDeclaredAnthropic(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":" Starship flew. "},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	ans, err := WebSearch(context.Background(), WebSearchConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "claude-opus-5-5", Native: domain.NativeWebSearchAnthropic,
	}, "spacex")
	if err != nil {
		t.Fatal(err)
	}
	if ans != "Starship flew." {
		t.Fatalf("answer = %q", ans)
	}
	tools, _ := sent["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != domain.AnthropicWebSearchFiltering {
		t.Fatalf("tools = %v", sent["tools"])
	}
	if _, has := sent["enable_search"]; has {
		t.Fatal("enable_search sent beside the declared tool")
	}
}
