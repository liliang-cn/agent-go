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

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Built-in `web_search` tool: real-time, grounded answers (news/finance/facts)
// via a search-capable, OpenAI-compatible chat endpoint. Undeclared, it sends
// both DashScope's enable_search and the OpenAI-style web_search_options, so a
// provider that supports either will ground the reply; Native names the one
// field the endpoint actually honours. The model is asked to answer concisely
// and cite sources.

// WebSearchConfig configures the built-in web_search tool. All three fields are
// required for the tool to be registered.
type WebSearchConfig struct {
	BaseURL string // OpenAI-compatible base, e.g. https://dashscope.aliyuncs.com/compatible-mode/v1
	APIKey  string
	Model   string // a search-capable model, e.g. qwen-plus
	// Native names the field that turns the endpoint's search on (see
	// domain.NativeWebSearchFormat). Empty sends both enable_search and
	// web_search_options. A Gemini gateway needs google_search: it accepts the
	// other two and ignores them, so the call answers from the model's own
	// knowledge — slowly, and "beyond my cutoff" — and the agent searches
	// again some other way. Anthropic's search is a server tool on its
	// Messages API, so "anthropic" goes through the pool client, which speaks
	// that API for api.anthropic.com.
	Native domain.NativeWebSearchFormat
	// ReasoningEffort is passed through when set; a search answer needs little.
	ReasoningEffort string
}

const webSearchPrompt = "Search the web, then answer the question below concisely and list 1-3 source links at the end:\n"

// buildWebSearchBody builds the chat-completions request body that triggers
// grounded retrieval. Pure function (no I/O) so it is unit-testable.
func buildWebSearchBody(cfg WebSearchConfig, query string) map[string]any {
	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": webSearchPrompt + query,
		}},
		"max_tokens": 700,
	}
	switch cfg.Native {
	case domain.NativeWebSearchGoogle:
		body["tools"] = []map[string]any{{"google_search": map[string]any{}}}
	case domain.NativeWebSearchDashScope:
		body["enable_search"] = true
	case domain.NativeWebSearchOpenAI:
		body["web_search_options"] = map[string]any{}
	default:
		body["enable_search"] = true                  // DashScope
		body["web_search_options"] = map[string]any{} // OpenAI-style
	}
	if e := strings.TrimSpace(cfg.ReasoningEffort); e != "" {
		body["reasoning_effort"] = e
	}
	return body
}

// WebSearch performs a grounded web search via the configured endpoint and
// returns the answer text.
func WebSearch(ctx context.Context, cfg WebSearchConfig, query string) (string, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return "", fmt.Errorf("web search not configured")
	}
	if cfg.Native == domain.NativeWebSearchAnthropic {
		return anthropicWebSearch(ctx, cfg, query)
	}
	body, _ := json.Marshal(buildWebSearchBody(cfg, query))
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

// anthropicWebSearch asks a Claude model with Anthropic's web search tool
// for the answer. A turn the search pauses is resumed by the transport, so
// the text that comes back is the whole answer.
func anthropicWebSearch(ctx context.Context, cfg WebSearchConfig, query string) (string, error) {
	c, err := pool.NewClient("web_search", cfg.BaseURL, cfg.APIKey, cfg.Model)
	if err != nil {
		return "", err
	}
	if err := c.SetNativeWebSearch(domain.NativeWebSearchAnthropic); err != nil {
		return "", err
	}
	if e := strings.TrimSpace(cfg.ReasoningEffort); e != "" {
		c.SetReasoningEffort(e)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := c.GenerateWithTools(ctx, []domain.Message{{Role: "user", Content: webSearchPrompt + query}}, nil,
		&domain.GenerationOptions{MaxTokens: 8000, WebSearchMode: domain.WebSearchModeNative})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Content) == "" {
		return "", fmt.Errorf("no search result")
	}
	return strings.TrimSpace(res.Content), nil
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
		"Search the web for current information (news, finance, stock prices, facts) and return a short answer with sources. Use it when the user asks for something recent, live, or absent from memory and records. To read the body text of one specific URL, use fetch_url instead.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search keywords or question"},
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
