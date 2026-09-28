package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// A provider's declaration decides the tool surface of the agent built over
// it, with no evidence gathered first.
func TestDeclaredProviderDecidesSurface(t *testing.T) {
	cases := []struct {
		format      domain.NativeWebSearchFormat
		wantSurface domain.WebSearchMode
		wantHideMCP bool
	}{
		// Declared support sends the field but keeps both routes: a model
		// with native search may still decline to use it.
		{domain.NativeWebSearchGoogle, domain.WebSearchModeAuto, false},
		{domain.NativeWebSearchDashScope, domain.WebSearchModeAuto, false},
		{domain.NativeWebSearchNone, domain.WebSearchModeMCP, false},
		{domain.NativeWebSearchUndeclared, domain.WebSearchModeAuto, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			c, err := pool.NewClient("cpa", "http://x", "k", "gemini")
			if err != nil {
				t.Fatal(err)
			}
			if err := c.SetNativeWebSearch(tc.format); err != nil {
				t.Fatal(err)
			}
			svc := autoModeService(c)
			if got := svc.webSearchSurfaceMode(); got != tc.wantSurface {
				t.Errorf("surface mode = %v, want %v", got, tc.wantSurface)
			}
			if got := svc.shouldHideMCPWebSearchTools(); got != tc.wantHideMCP {
				t.Errorf("hide MCP search tools = %v, want %v", got, tc.wantHideMCP)
			}
		})
	}
}

// Grounding evidence in a response proves a declared provider really searches,
// and only then do the MCP search tools go away.
func TestDeclaredSupportHidesMCPOnlyOnceProven(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, _ := json.Marshal(map[string]any{"choices": []map[string]any{{
			"message": map[string]any{"role": "assistant", "content": "Paris.",
				"annotations": []map[string]any{{"type": "url_citation"}}},
			"finish_reason": "stop",
		}}})
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	c, err := pool.NewClient("dash", srv.URL, "k", "qwen")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetNativeWebSearch(domain.NativeWebSearchDashScope, map[string]interface{}{"forced_search": true}); err != nil {
		t.Fatal(err)
	}
	svc := autoModeService(c)
	if svc.shouldHideMCPWebSearchTools() {
		t.Fatal("declared-only support hid the MCP search tools")
	}
	if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil,
		&domain.GenerationOptions{WebSearchMode: domain.WebSearchModeAuto}); err != nil {
		t.Fatal(err)
	}
	if !c.NativeWebSearchProven() {
		t.Fatal("grounding evidence did not prove the declared support")
	}
	if got := svc.webSearchSurfaceMode(); got != domain.WebSearchModeNative {
		t.Fatalf("surface after proof = %v, want native", got)
	}
	if !svc.shouldHideMCPWebSearchTools() {
		t.Fatal("proven support still offers the MCP search tools")
	}
}

// End to end: an agent built with WithLLM over a client declared
// google_search asks its upstream for Google Search on a real run, and sends
// neither of the fields an undeclared provider would get.
func TestDeclaredGoogleSearchReachesTheWire(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		out, _ := json.Marshal(map[string]any{"choices": []map[string]any{{
			"message":       map[string]any{"role": "assistant", "content": "Paris."},
			"finish_reason": "stop",
		}}})
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	c, err := pool.NewClient("cpa", srv.URL, "k", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetNativeWebSearch(domain.NativeWebSearchGoogle); err != nil {
		t.Fatal(err)
	}
	svc, err := New("researcher").WithConfig(testAgentConfig(t.TempDir())).WithLLM(c).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if _, err := svc.Run(context.Background(), "What is the capital of France?", WithConstraintExtraction(false)); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	sawGoogle := false
	for _, b := range bodies {
		if _, has := b["web_search_options"]; has {
			t.Fatal("declared google_search still sent web_search_options")
		}
		if _, has := b["enable_search"]; has {
			t.Fatal("declared google_search still sent enable_search")
		}
		tools, _ := b["tools"].([]any)
		for _, tl := range tools {
			if m, ok := tl.(map[string]any); ok {
				if _, ok := m["google_search"]; ok {
					sawGoogle = true
				}
			}
		}
	}
	if !sawGoogle {
		t.Fatalf("no request carried google_search (%d requests)", len(bodies))
	}
}
