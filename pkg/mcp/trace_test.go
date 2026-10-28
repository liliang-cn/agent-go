package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// A tool call carries the calling span to the MCP server — in params._meta,
// which any transport delivers, and as the HTTP header — and the server's own
// span, when it answers with one, is noted as the call's remote child.
func TestCallToolCarriesTheFlowBothWays(t *testing.T) {
	var mu sync.Mutex
	var metaTP, headerTP, state string
	const serverSpan = "00-4bf92f3577b34da6a3ce929d0e0e4736-2222222222222222-01"
	srv := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
			mu.Lock()
			metaTP, _ = req.Params.Meta["traceparent"].(string)
			state, _ = req.Params.Meta["tracestate"].(string)
			mu.Unlock()
			return &mcp.CallToolResult{Meta: mcp.Meta{"traceparent": serverSpan},
				Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tp := r.Header.Get("traceparent"); tp != "" {
			mu.Lock()
			headerTP = tp
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	defer s.Close()

	c := newTestHTTPClient(t, s.URL)
	require.NoError(t, c.Connect(context.Background()))
	defer c.Close()

	ctx := trace.Extract(context.Background(), strMap{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "tracestate": "cortex=1",
	}.get)
	ctx, tool := trace.StartSpan(ctx)
	ctx, links := trace.WithLinks(ctx)
	res, err := c.CallTool(ctx, "echo", map[string]interface{}{"text": "hi"})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, tool.Traceparent(), metaTP, "params._meta.traceparent")
	require.Equal(t, "cortex=1", state, "params._meta.tracestate")
	require.Equal(t, tool.Traceparent(), headerTP, "traceparent header")
	require.Equal(t, []string{serverSpan}, links.List())
}

type strMap map[string]string

func (m strMap) get(k string) string { return m[k] }
