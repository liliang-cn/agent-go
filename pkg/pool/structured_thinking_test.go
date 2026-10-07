package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// thinkingServer plays a reasoning model: it reasons unless asked not to,
// and, when refuses is set, rejects the field that asks.
type thinkingServer struct {
	mu       sync.Mutex
	refuses  bool
	thinking []string // the thinking type each request carried, "" for none
}

func (s *thinkingServer) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	typ := ""
	if th, ok := body["thinking"].(map[string]interface{}); ok {
		typ, _ = th["type"].(string)
	}
	s.thinking = append(s.thinking, typ)
	if typ != "" && s.refuses {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"Unrecognized request argument supplied: thinking"}}`)
		return
	}
	reasoning := "Let me look at the index first."
	if typ == "disabled" {
		reasoning = ""
	}
	fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"ok\":true}","reasoning_content":%q}}]}`, reasoning)
}

func newThinkingClient(t *testing.T, s *thinkingServer) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	c, err := NewClient("p", srv.URL, "", "m")
	if err != nil {
		t.Fatal(err)
	}
	c.structuredSchemaRejected.Store(true)
	return c
}

func structuredTimes(t *testing.T, c *Client, n int, opts *domain.GenerationOptions) {
	t.Helper()
	for i := 0; i < n; i++ {
		res, err := c.GenerateStructured(context.Background(), "pick", map[string]any{"type": "object"}, opts)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if res.Raw != `{"ok":true}` {
			t.Fatalf("call %d: raw = %q", i, res.Raw)
		}
	}
}

var quick = &domain.GenerationOptions{NoReasoning: true}

// A call that wants no reasoning sends nothing new to an upstream that has
// never reasoned; once a reply has reasoned, it asks for none.
func TestStructuredTurnsReasoningOffOnceSeen(t *testing.T) {
	s := &thinkingServer{}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 3, quick)
	if want := "[ disabled disabled]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
}

// An upstream that refuses the field is asked again without it, and the
// field is not sent to it again.
func TestStructuredRemembersAThinkingRejection(t *testing.T) {
	s := &thinkingServer{refuses: true}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 3, quick)
	if want := "[ disabled  ]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
}

// A caller that says how to reason is obeyed, reasoning seen or not.
func TestStructuredKeepsAnExplicitThinkingChoice(t *testing.T) {
	s := &thinkingServer{}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 2, &domain.GenerationOptions{Thinking: &domain.ThinkingOptions{Type: "enabled"}})
	if want := "[enabled enabled]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
}

// A call that does not ask is left to reason: extraction run without it
// misattributed what was said.
func TestStructuredReasonsByDefault(t *testing.T) {
	s := &thinkingServer{}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 3, nil)
	if want := "[  ]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
}
