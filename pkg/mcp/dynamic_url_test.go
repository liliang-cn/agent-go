package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// A server reached by URL is added while the service runs, with the header
// it wants; adding it again under the same name points it somewhere else.
func TestAddDynamicServerConfigByURL(t *testing.T) {
	guarded := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer s3cret" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	first := httptest.NewServer(guarded(newEchoHandler("one:", false)))
	defer first.Close()
	second := httptest.NewServer(guarded(newEchoHandler("two:", true)))
	defer second.Close()
	pinger := mcp.NewServer(&mcp.Implementation{Name: "sse", Version: "1"}, nil)
	mcp.AddTool(pinger, &mcp.Tool{Name: "ping"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
	})
	sse := httptest.NewServer(guarded(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return pinger }, nil)))
	defer sse.Close()
	defer sse.CloseClientConnections()

	cfg := DefaultConfig()
	cfg.Servers = nil
	svc, err := NewService(&cfg, nil)
	require.NoError(t, err)
	defer svc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	require.Error(t, svc.AddDynamicServerConfig(ctx, ServerConfig{Name: "remote", Type: ServerTypeHTTP, URL: first.URL}),
		"connected without the token")
	require.NoError(t, svc.AddDynamicServerConfig(ctx, ServerConfig{Name: "remote", Type: ServerTypeHTTP, URL: first.URL,
		Headers: map[string]string{"Authorization": "Bearer s3cret"}}))
	res, err := svc.CallTool(ctx, "mcp_remote_echo", map[string]any{"text": "hi"})
	require.NoError(t, err)
	require.Contains(t, res.Data, "one:hi")

	require.NoError(t, svc.AddDynamicServerConfig(ctx, ServerConfig{Name: "remote", Type: ServerTypeHTTP, URL: second.URL,
		Headers: map[string]string{"Authorization": "Bearer s3cret"}}))
	res, err = svc.CallTool(ctx, "mcp_remote_echo", map[string]any{"text": "hi"})
	require.NoError(t, err)
	require.Contains(t, res.Data, "two:hi")
	n := 0
	for _, s := range cfg.GetLoadedServers() {
		if s.Name == "remote" {
			n++
		}
	}
	require.Equal(t, 1, n, "the server is loaded once")

	require.NoError(t, svc.AddDynamicServerConfig(ctx, ServerConfig{Name: "events", Type: ServerTypeSSE, URL: sse.URL,
		Headers: map[string]string{"Authorization": "Bearer s3cret"}}))
	res, err = svc.CallTool(ctx, "mcp_events_ping", map[string]any{"text": "x"})
	require.NoError(t, err)
	require.Contains(t, res.Data, "pong")

	require.Error(t, svc.AddDynamicServerConfig(ctx, ServerConfig{Name: "nowhere", Type: ServerTypeHTTP}))
	require.Error(t, svc.AddDynamicServerConfig(ctx, ServerConfig{Name: "odd", Type: "carrier-pigeon", URL: "x"}))
}
