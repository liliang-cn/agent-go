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
	refuses  string   // "thinking", "reasoning_effort", "unnamed", or "" for none
	thinking []string // the thinking type each request carried, "" for none
	effort   []string // the reasoning_effort each request carried
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
	effort, _ := body["reasoning_effort"].(string)
	s.thinking = append(s.thinking, typ)
	s.effort = append(s.effort, effort)
	refuse := (s.refuses == "thinking" && typ != "") ||
		(s.refuses == "reasoning_effort" && effort != "") ||
		(s.refuses == "unnamed" && (typ != "" || effort != ""))
	if refuse {
		msg := "Unrecognized request argument supplied: " + s.refuses
		if s.refuses == "unnamed" {
			msg = "invalid request"
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"message":%q}}`, msg)
		return
	}
	reasoning := "Let me look at the index first."
	if typ == "disabled" || effort == "none" {
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

// A structured call turns reasoning off from its very first request, both
// halves of the switch, whether or not it asked.
func TestStructuredTurnsReasoningOffFromTheFirstCall(t *testing.T) {
	for _, opts := range []*domain.GenerationOptions{nil, quick, {Temperature: 0}} {
		s := &thinkingServer{}
		c := newThinkingClient(t, s)
		structuredTimes(t, c, 2, opts)
		if want := "[disabled disabled]"; fmt.Sprint(s.thinking) != want {
			t.Fatalf("opts %+v: thinking sent = %q, want %q", opts, fmt.Sprint(s.thinking), want)
		}
		if want := "[none none]"; fmt.Sprint(s.effort) != want {
			t.Fatalf("opts %+v: reasoning_effort sent = %q, want %q", opts, fmt.Sprint(s.effort), want)
		}
	}
}

// An upstream that refuses the thinking field is asked again without it,
// keeps getting reasoning_effort "none", and is not sent thinking again.
func TestStructuredRemembersAThinkingRejection(t *testing.T) {
	s := &thinkingServer{refuses: "thinking"}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 3, nil)
	if want := "[disabled   ]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
	if want := "[none none none none]"; fmt.Sprint(s.effort) != want {
		t.Fatalf("reasoning_effort sent = %q, want %q", fmt.Sprint(s.effort), want)
	}
}

// The same for reasoning_effort "none".
func TestStructuredRemembersAnEffortRejection(t *testing.T) {
	s := &thinkingServer{refuses: "reasoning_effort"}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 3, nil)
	if want := "[none   ]"; fmt.Sprint(s.effort) != want {
		t.Fatalf("reasoning_effort sent = %q, want %q", fmt.Sprint(s.effort), want)
	}
	if want := "[disabled disabled disabled disabled]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
}

// A 400 that names neither field is retried without both, and remembered
// only because the retry succeeded.
func TestStructuredUnnamedRejectionIsRememberedWhenTheRetryWorks(t *testing.T) {
	s := &thinkingServer{refuses: "unnamed"}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 2, nil)
	if want := "[disabled  ]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
}

// A caller that says how to reason is obeyed.
func TestStructuredKeepsAnExplicitThinkingChoice(t *testing.T) {
	s := &thinkingServer{}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 2, &domain.GenerationOptions{Thinking: &domain.ThinkingOptions{Type: "enabled"}})
	if want := "[enabled enabled]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
	if want := "[ ]"; fmt.Sprint(s.effort) != want {
		t.Fatalf("reasoning_effort sent = %q, want %q", fmt.Sprint(s.effort), want)
	}
}

// A call that asks to reason is sent neither half of the off switch:
// extraction run without it misattributed what was said.
func TestStructuredReasonsWhenAsked(t *testing.T) {
	s := &thinkingServer{}
	c := newThinkingClient(t, s)
	structuredTimes(t, c, 2, &domain.GenerationOptions{Reasoning: true})
	if want := "[ ]"; fmt.Sprint(s.thinking) != want {
		t.Fatalf("thinking sent = %q, want %q", fmt.Sprint(s.thinking), want)
	}
	if want := "[ ]"; fmt.Sprint(s.effort) != want {
		t.Fatalf("reasoning_effort sent = %q, want %q", fmt.Sprint(s.effort), want)
	}
}
