package agent

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Built-in `fetch_url` tool: read the actual readable text of a specific web
// page. It is the "read this page" counterpart to a web search (which returns
// search-engine summaries). Includes a basic SSRF guard since agents are often
// internet-facing.

const (
	fetchURLMaxBytes = 512 * 1024 // cap on bytes read from the response
	fetchURLMaxRunes = 8000       // cap on returned text length
)

var (
	reFetchScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\s*(script|style)\s*>`)
	reFetchTag         = regexp.MustCompile(`(?s)<[^>]+>`)
)

// FetchURL GETs an http(s) page and returns its readable text (HTML stripped,
// entities unescaped, whitespace collapsed, truncated). Refuses loopback /
// private / link-local hosts (SSRF guard).
func FetchURL(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("only http/https URLs are allowed")
	}
	if fetchHostBlocked(u.Hostname()) {
		return "", fmt.Errorf("refusing to fetch a private/loopback host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (agent-go fetch_url)")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, fetchURLMaxBytes))
	text := htmlToText(string(body))
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("page had no readable text (status %d)", resp.StatusCode)
	}
	return text, nil
}

// htmlToText strips script/style + tags, unescapes entities, collapses
// whitespace, and truncates. Pure function (no I/O) so it is unit-testable.
func htmlToText(body string) string {
	s := reFetchScriptStyle.ReplaceAllString(body, " ")
	s = reFetchTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > fetchURLMaxRunes {
		s = string(r[:fetchURLMaxRunes]) + "…"
	}
	return s
}

// fetchHostBlocked reports whether host resolves to a loopback/private/
// link-local/unspecified address (SSRF guard). IP literals need no DNS.
func fetchHostBlocked(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false // let the request fail naturally if it won't resolve
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// RegisterFetchURLTool registers the built-in `fetch_url` tool on a service so
// the agent loop can read a specific page's text (e.g. to review or quote it).
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterFetchURLTool(svc)
func RegisterFetchURLTool(svc *Service) {
	if svc == nil {
		return
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("fetch_url") {
		return
	}
	svc.AddToolWithMetadata(
		"fetch_url",
		"抓取指定网址的正文文本,用于阅读/审阅/引用某个具体页面的真实内容。与网页搜索不同:搜索给摘要,fetch_url 读这个 URL 的实际页面文字。仅支持 http(s)。",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{"type": "string", "description": "完整网址,http:// 或 https:// 开头"},
			},
			"required": []string{"url"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			u := toolArgString(args, "url")
			if u == "" {
				return map[string]interface{}{"ok": false, "error": "url required"}, nil
			}
			text, err := FetchURL(ctx, u)
			if err != nil {
				return map[string]interface{}{"ok": false, "error": err.Error()}, nil
			}
			return map[string]interface{}{"ok": true, "data": map[string]interface{}{"url": u, "text": text}}, nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}
