package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Built-in `web_search` tool: real-time, grounded answers (news/finance/facts)
// via a search-capable, OpenAI-compatible chat endpoint. It sends both
// DashScope's enable_search and the OpenAI-style web_search_options, so a
// provider that supports either will ground the reply; the model is asked to
// answer concisely and cite sources.

// WebSearchConfig configures the built-in web_search tool. All three fields are
// required for the tool to be registered.
type WebSearchConfig struct {
	BaseURL string // OpenAI-compatible base, e.g. https://dashscope.aliyuncs.com/compatible-mode/v1
	APIKey  string
	Model   string // a search-capable model, e.g. qwen-plus
}

// buildWebSearchBody builds the chat-completions request body that triggers
// grounded retrieval. Pure function (no I/O) so it is unit-testable.
func buildWebSearchBody(model, query string) map[string]any {
	return map[string]any{
		"model": model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "联网搜索后用简洁的语言回答下面的问题,并在末尾附 1-3 个来源链接:\n" + query,
		}},
		"enable_search":      true,             // DashScope
		"web_search_options": map[string]any{}, // OpenAI-style
		"max_tokens":         700,
	}
}

// WebSearch performs a grounded web search via the configured endpoint and
// returns the answer text.
func WebSearch(ctx context.Context, cfg WebSearchConfig, query string) (string, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return "", fmt.Errorf("web search not configured")
	}
	body, _ := json.Marshal(buildWebSearchBody(cfg.Model, query))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no search result")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// RegisterWebSearchTool registers the built-in `web_search` tool on a service.
// It is a no-op when cfg is incomplete (so callers can wire it conditionally
// from env without branching).
//
//	agent.RegisterWebSearchTool(svc, agent.WebSearchConfig{
//	    BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
//	    APIKey:  os.Getenv("DASHSCOPE_API_KEY"), Model: "qwen-plus",
//	})
func RegisterWebSearchTool(svc *Service, cfg WebSearchConfig) {
	if svc == nil {
		return
	}
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return // not configured
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("web_search") {
		return
	}
	svc.AddToolWithMetadata(
		"web_search",
		"联网搜索实时信息(新闻/财经/股价/事实等),返回简要答案与来源。当用户要查最新、实时、或不在记忆与记录里的信息时调用。读取某个具体网址的正文请改用 fetch_url。",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "搜索关键词或问题"},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			q := toolArgString(args, "query")
			if q == "" {
				return map[string]interface{}{"ok": false, "error": "query required"}, nil
			}
			ans, err := WebSearch(ctx, cfg, q)
			if err != nil {
				return map[string]interface{}{"ok": false, "error": err.Error()}, nil
			}
			return map[string]interface{}{"ok": true, "data": map[string]interface{}{"query": q, "answer": ans}}, nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}
