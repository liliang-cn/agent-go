package pool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Each declared format sends exactly its own field, and nothing else.
func TestDeclaredFormatSendsOnlyItsField(t *testing.T) {
	cases := []struct {
		format        domain.NativeWebSearchFormat
		webSearchOpts bool
		enableSearch  bool
		googleSearch  bool
	}{
		{domain.NativeWebSearchUndeclared, true, true, false},
		{domain.NativeWebSearchNone, false, false, false},
		{domain.NativeWebSearchOpenAI, true, false, false},
		{domain.NativeWebSearchDashScope, false, true, false},
		{domain.NativeWebSearchGoogle, false, false, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			req := buildPoolGenerateWithToolsRequest("m", nil, nil, autoOpts(), tc.format)
			_, hasOpts := req["web_search_options"]
			_, hasEnable := req["enable_search"]
			if hasOpts != tc.webSearchOpts || hasEnable != tc.enableSearch || hasGoogleSearchTool(req) != tc.googleSearch {
				t.Fatalf("web_search_options=%v enable_search=%v google_search=%v, want %v %v %v",
					hasOpts, hasEnable, hasGoogleSearchTool(req), tc.webSearchOpts, tc.enableSearch, tc.googleSearch)
			}
		})
	}
}

// google_search is a built-in tool: it joins the function tools, it does not
// replace them, and it goes last so the function tools' cache prefix is
// unchanged.
func TestGoogleSearchJoinsFunctionTools(t *testing.T) {
	tools := []domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "fs_read"}}}
	req := buildPoolGenerateWithToolsRequest("m", nil, tools, autoOpts(), domain.NativeWebSearchGoogle)
	list, _ := req["tools"].([]map[string]interface{})
	if len(list) != 2 {
		t.Fatalf("tools = %d entries, want 2", len(list))
	}
	if list[0]["type"] != "function" {
		t.Fatalf("first tool = %v, want the function tool", list[0])
	}
	if _, ok := list[1]["google_search"]; !ok {
		t.Fatalf("last tool = %v, want google_search", list[1])
	}
}

// A request that did not ask for native search gets no search field, whatever
// the declaration — a declaration says what the provider can do, not that
// every call must search.
func TestDeclarationDoesNotForceSearch(t *testing.T) {
	req := buildPoolGenerateWithToolsRequest("m", nil, nil, &domain.GenerationOptions{}, domain.NativeWebSearchGoogle)
	if hasGoogleSearchTool(req) {
		t.Fatal("google_search sent on a request that did not ask for native search")
	}
}

func TestDeclarationSetsVerdictFromTheStart(t *testing.T) {
	cases := map[domain.NativeWebSearchFormat][2]bool{ // supported, known
		domain.NativeWebSearchUndeclared: {false, false},
		domain.NativeWebSearchNone:       {false, true},
		domain.NativeWebSearchOpenAI:     {true, true},
		domain.NativeWebSearchDashScope:  {true, true},
		domain.NativeWebSearchGoogle:     {true, true},
	}
	for format, want := range cases {
		c, err := NewClient("p", "http://x", "k", "m")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetNativeWebSearch(format); err != nil {
			t.Fatal(err)
		}
		if s, k := c.NativeWebSearchVerdict(); s != want[0] || k != want[1] {
			t.Errorf("%q: verdict = (%v, %v), want (%v, %v)", format, s, k, want[0], want[1])
		}
	}
}

// Silence never moves a declared verdict — a gateway that strips grounding
// metadata could otherwise never prove it — and neither does evidence against
// "none". A rejection of the declared field does: honouring the declaration
// past it would hide MCP search behind a search that cannot run.
func TestDeclarationAndEvidence(t *testing.T) {
	none := newNativeSearchState("p", domain.NativeWebSearchNone, nil)
	none.markSupported()
	if s, k := none.verdict(); s || !k {
		t.Fatalf("declared none after grounding evidence = (%v, %v), want (false, true)", s, k)
	}

	google := newNativeSearchState("p", domain.NativeWebSearchGoogle, nil)
	if s, k := google.verdict(); !s || !k {
		t.Fatalf("declared google_search before any request = (%v, %v), want (true, true)", s, k)
	}
	google.markUnsupported()
	if s, k := google.verdict(); s || !k {
		t.Fatalf("declared google_search after a rejection = (%v, %v), want (false, true)", s, k)
	}
}

// The failure measured on cpa: Gemini refuses google_search beside function
// tools. The round must survive it — the search field is dropped, the tools
// are kept, the retry succeeds — and the provider falls back to unsupported.
func TestDeclaredGoogleSearchRejectedBesideFunctionTools(t *testing.T) {
	var bodies []map[string]any
	c := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		for _, tl := range body["tools"].([]any) {
			if _, ok := tl.(map[string]any)["google_search"]; ok {
				http.Error(w, `{"error":{"code":400,"message":"Please enable tool_config.include_server_side_tool_invocations to use Built-in tools with Function calling.","status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
				return
			}
		}
		_, _ = w.Write([]byte(plainCompletion("ok")))
	})
	if err := c.SetNativeWebSearch(domain.NativeWebSearchGoogle); err != nil {
		t.Fatal(err)
	}
	tools := []domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "fs_read"}}}
	if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, tools, autoOpts()); err != nil {
		t.Fatalf("round failed instead of degrading: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the rejected one and a retry", len(bodies))
	}
	retried, _ := bodies[1]["tools"].([]any)
	if len(retried) != 1 {
		t.Fatalf("retry tools = %v, want only the function tool", bodies[1]["tools"])
	}
	if s, k := c.NativeWebSearchVerdict(); s || !k {
		t.Fatalf("verdict after rejection = (%v, %v), want (false, true)", s, k)
	}
}

// A typo in a declaration fails loudly instead of becoming "undeclared".
func TestUnknownDeclarationFailsPoolConstruction(t *testing.T) {
	_, err := NewPool(PoolConfig{Enabled: true, Providers: []Provider{{
		Name: "cpa", BaseURL: "http://x", ModelName: "m", NativeWebSearch: "gogle_search",
	}}})
	if err == nil || !strings.Contains(err.Error(), "gogle_search") {
		t.Fatalf("err = %v, want an error naming the bad value", err)
	}
}

// The declaration reaches the wire through the pool, and a model-override
// client derived from the same provider inherits it.
func TestProviderDeclarationReachesTheWire(t *testing.T) {
	var body map[string]any
	srv := newJSONServer(t, func(b map[string]any) string {
		body = b
		return plainCompletion("ok")
	})
	p, err := NewPool(PoolConfig{Enabled: true, Providers: []Provider{{
		Name: "cpa", BaseURL: srv, ModelName: "gemini-a", Models: []string{"gemini-a", "gemini-b"},
		NativeWebSearch: "google_search",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.GetByProviderAndModel("cpa", "gemini-b")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release(c)
	if s, k := c.NativeWebSearchVerdict(); !s || !k {
		t.Fatalf("derived client verdict = (%v, %v), want declared (true, true)", s, k)
	}
	if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, autoOpts()); err != nil {
		t.Fatal(err)
	}
	tools, _ := body["tools"].([]any)
	found := false
	for _, tl := range tools {
		if m, ok := tl.(map[string]any); ok {
			if _, ok := m["google_search"]; ok {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("request tools = %v, want a google_search entry", body["tools"])
	}
	if _, has := body["web_search_options"]; has {
		t.Fatal("declared google_search still sent web_search_options")
	}
}

func hasGoogleSearchTool(req map[string]interface{}) bool {
	list, _ := req["tools"].([]map[string]interface{})
	for _, tl := range list {
		if _, ok := tl["google_search"]; ok {
			return true
		}
	}
	return false
}

func newJSONServer(t *testing.T, reply func(map[string]any) string) string {
	t.Helper()
	c := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(reply(body)))
	})
	return c.GetBaseURL()
}

// Declared options go inside the declared field, and nowhere else.
func TestDeclaredOptionsLandInTheirField(t *testing.T) {
	opts := map[string]interface{}{"forced_search": true}

	dash := buildPoolGenerateWithToolsRequest("m", nil, nil, autoOpts(), domain.NativeWebSearchDashScope, opts)
	if dash["enable_search"] != true {
		t.Fatal("dashscope: enable_search missing")
	}
	so, _ := dash["search_options"].(map[string]interface{})
	if so["forced_search"] != true {
		t.Fatalf("dashscope: search_options = %v", dash["search_options"])
	}

	oa := buildPoolGenerateWithToolsRequest("m", nil, nil, autoOpts(), domain.NativeWebSearchOpenAI, map[string]interface{}{"user_location": "CN"})
	wso, _ := oa["web_search_options"].(map[string]interface{})
	if wso["user_location"] != "CN" || wso["search_context_size"] == nil {
		t.Fatalf("openai: web_search_options = %v", oa["web_search_options"])
	}

	g := buildPoolGenerateWithToolsRequest("m", nil, nil, autoOpts(), domain.NativeWebSearchGoogle, opts)
	list, _ := g["tools"].([]map[string]interface{})
	cfg, _ := list[len(list)-1]["google_search"].(map[string]interface{})
	if cfg["forced_search"] != true {
		t.Fatalf("google: tool config = %v", list)
	}

	plain := buildPoolGenerateWithToolsRequest("m", nil, nil, autoOpts(), domain.NativeWebSearchDashScope)
	if _, has := plain["search_options"]; has {
		t.Fatal("search_options sent with no options declared")
	}
}

// Options need a declared field to go into.
func TestOptionsWithoutAFieldAreRefused(t *testing.T) {
	c, err := NewClient("p", "http://x", "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []domain.NativeWebSearchFormat{domain.NativeWebSearchUndeclared, domain.NativeWebSearchNone} {
		if err := c.SetNativeWebSearch(f, map[string]interface{}{"forced_search": true}); err == nil {
			t.Errorf("%q accepted options it has nowhere to send", f)
		}
	}
	if _, err := NewPool(PoolConfig{Enabled: true, Providers: []Provider{{
		Name: "p", BaseURL: "http://x", ModelName: "m",
		NativeWebSearchOptions: map[string]interface{}{"forced_search": true},
	}}}); err == nil {
		t.Error("pool accepted options on an undeclared provider")
	}
}
