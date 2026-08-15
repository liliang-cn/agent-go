package pool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func nativeSearchTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient("p", srv.URL, "k", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func autoOpts() *domain.GenerationOptions {
	return &domain.GenerationOptions{WebSearchMode: domain.WebSearchModeAuto}
}

func plainCompletion(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	})
	return string(b)
}

// A rejection of the web-search parameters, followed by a successful retry,
// proves unsupported — and the retried request no longer carries them.
func TestVerdictUnsupportedOnRejection(t *testing.T) {
	var bodies []map[string]any
	c := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		if _, has := body["web_search_options"]; has {
			http.Error(w, `{"error":{"message":"unsupported parameter: web_search_options"}}`, http.StatusBadRequest)
			return
		}
		w.Write([]byte(plainCompletion("ok")))
	})

	if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, autoOpts()); err != nil {
		t.Fatal(err)
	}
	if supported, known := c.NativeWebSearchVerdict(); !known || supported {
		t.Fatalf("verdict = (%v, %v), want (false, true)", supported, known)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 requests (send + stripped retry), got %d", len(bodies))
	}
	if _, has := bodies[1]["web_search_options"]; has {
		t.Fatal("retry still carried web_search_options")
	}
}

// Grounding evidence in the response proves supported.
func TestVerdictSupportedOnGroundingEvidence(t *testing.T) {
	cases := map[string]string{
		"openai url_citation annotations": `{"choices":[{"message":{"role":"assistant","content":"x","annotations":[{"type":"url_citation","url_citation":{"url":"https://a"}}]},"finish_reason":"stop"}]}`,
		"gemini grounding_metadata":       `{"choices":[{"message":{"role":"assistant","content":"x"},"grounding_metadata":{"webSearchQueries":["q"]},"finish_reason":"stop"}]}`,
		"gemini groundingMetadata camel":  `{"choices":[{"message":{"role":"assistant","content":"x"},"groundingMetadata":{"webSearchQueries":["q"]},"finish_reason":"stop"}]}`,
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			c := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(resp))
			})
			if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, autoOpts()); err != nil {
				t.Fatal(err)
			}
			if supported, known := c.NativeWebSearchVerdict(); !known || !supported {
				t.Fatalf("verdict = (%v, %v), want (true, true)", supported, known)
			}
		})
	}
}

// Mere acceptance proves nothing: most servers silently ignore unknown
// fields, and treating that as support would hide the MCP search tools
// behind a capability that does not exist.
func TestVerdictStaysUnknownOnSilentAcceptance(t *testing.T) {
	c := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(plainCompletion("ok, no grounding here")))
	})
	if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, autoOpts()); err != nil {
		t.Fatal(err)
	}
	if _, known := c.NativeWebSearchVerdict(); known {
		t.Fatal("silent acceptance must not produce a verdict")
	}
}

// A later rejection overrides an earlier supported verdict: a heterogeneous
// upstream that only sometimes searches natively cannot be relied on.
func TestRejectionOverridesSupported(t *testing.T) {
	s := &nativeSearchState{}
	s.markSupported()
	s.markUnsupported()
	if supported, known := s.verdict(); !known || supported {
		t.Fatalf("verdict = (%v, %v), want (false, true)", supported, known)
	}
	// And supported never upgrades out of unsupported.
	s.markSupported()
	if supported, _ := s.verdict(); supported {
		t.Fatal("markSupported overrode a recorded rejection")
	}
}

func TestPoolVerdictAggregation(t *testing.T) {
	mk := func(v int32) *clientWrapper {
		c := &Client{nativeSearch: &nativeSearchState{}}
		c.nativeSearch.v.Store(v)
		return &clientWrapper{client: c}
	}
	cases := []struct {
		name          string
		clients       map[string]*clientWrapper
		wantSupported bool
		wantKnown     bool
	}{
		{"all unknown", map[string]*clientWrapper{"a": mk(nativeSearchUnknown)}, false, false},
		{"one supported", map[string]*clientWrapper{"a": mk(nativeSearchSupported), "b": mk(nativeSearchUnknown)}, true, true},
		{"mixed with unsupported", map[string]*clientWrapper{"a": mk(nativeSearchSupported), "b": mk(nativeSearchUnsupported)}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pool{clients: tc.clients}
			supported, known := p.NativeWebSearchVerdict()
			if supported != tc.wantSupported || known != tc.wantKnown {
				t.Fatalf("verdict = (%v, %v), want (%v, %v)", supported, known, tc.wantSupported, tc.wantKnown)
			}
		})
	}
}
