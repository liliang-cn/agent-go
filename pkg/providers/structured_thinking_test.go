package providers

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

// thinkingUpstream plays a reasoning model behind an OpenAI-compatible API,
// recording each request's off switch and refusing the field named in
// refuses ("thinking", "reasoning_effort", "unnamed" for a bare 400).
type thinkingUpstream struct {
	mu       sync.Mutex
	refuses  string
	thinking []string
	effort   []string
}

func (u *thinkingUpstream) provider(t *testing.T) *OpenAILLMProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		defer u.mu.Unlock()
		typ := ""
		if th, ok := body["thinking"].(map[string]interface{}); ok {
			typ, _ = th["type"].(string)
		}
		effort, _ := body["reasoning_effort"].(string)
		u.thinking = append(u.thinking, typ)
		u.effort = append(u.effort, effort)
		w.Header().Set("Content-Type", "application/json")
		refuse := (u.refuses == "thinking" && typ != "") ||
			(u.refuses == "reasoning_effort" && effort != "") ||
			(u.refuses == "unnamed" && (typ != "" || effort != ""))
		if refuse {
			msg := "Unrecognized request argument supplied: " + u.refuses
			if u.refuses == "unnamed" {
				msg = "invalid request"
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":%q,"type":"invalid_request_error"}}`, msg)
			return
		}
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(srv.Close)
	gen, err := NewOpenAILLMProvider(&domain.OpenAIProviderConfig{BaseURL: srv.URL, APIKey: "k", LLMModel: "m"})
	if err != nil {
		t.Fatal(err)
	}
	return gen.(*OpenAILLMProvider)
}

func (u *thinkingUpstream) call(t *testing.T, p *OpenAILLMProvider, n int, opts *domain.GenerationOptions) {
	t.Helper()
	for i := 0; i < n; i++ {
		var out map[string]interface{}
		res, err := p.GenerateStructured(context.Background(), "pick", &out, opts)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if res.Raw != `{"ok":true}` {
			t.Fatalf("call %d: raw = %q", i, res.Raw)
		}
	}
}

func (u *thinkingUpstream) check(t *testing.T, wantThinking, wantEffort string) {
	t.Helper()
	if got := fmt.Sprint(u.thinking); got != wantThinking {
		t.Errorf("thinking sent = %q, want %q", got, wantThinking)
	}
	if got := fmt.Sprint(u.effort); got != wantEffort {
		t.Errorf("reasoning_effort sent = %q, want %q", got, wantEffort)
	}
}

func TestProviderStructuredTurnsReasoningOffFromTheFirstCall(t *testing.T) {
	for _, opts := range []*domain.GenerationOptions{nil, {Temperature: 0}, {NoReasoning: true}} {
		u := &thinkingUpstream{}
		u.call(t, u.provider(t), 2, opts)
		u.check(t, "[disabled disabled]", "[none none]")
	}
}

func TestProviderStructuredRemembersAThinkingRejection(t *testing.T) {
	u := &thinkingUpstream{refuses: "thinking"}
	u.call(t, u.provider(t), 3, nil)
	u.check(t, "[disabled   ]", "[none none none none]")
}

func TestProviderStructuredRemembersAnEffortRejection(t *testing.T) {
	u := &thinkingUpstream{refuses: "reasoning_effort"}
	u.call(t, u.provider(t), 3, nil)
	u.check(t, "[disabled disabled disabled disabled]", "[none   ]")
}

func TestProviderStructuredUnnamedRejectionDropsBoth(t *testing.T) {
	u := &thinkingUpstream{refuses: "unnamed"}
	u.call(t, u.provider(t), 2, nil)
	u.check(t, "[disabled  ]", "[none  ]")
}

func TestProviderStructuredKeepsAnExplicitThinkingChoice(t *testing.T) {
	u := &thinkingUpstream{}
	u.call(t, u.provider(t), 2, &domain.GenerationOptions{Thinking: &domain.ThinkingOptions{Type: "enabled"}})
	u.check(t, "[enabled enabled]", "[ ]")
}

func TestProviderStructuredReasonsWhenAsked(t *testing.T) {
	u := &thinkingUpstream{}
	u.call(t, u.provider(t), 2, &domain.GenerationOptions{Reasoning: true})
	u.check(t, "[ ]", "[ ]")
}
