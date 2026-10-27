package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

type wireSeen struct {
	mu sync.Mutex
	ev []trace.WireEvent
}

func (s *wireSeen) on(e trace.WireEvent) { s.mu.Lock(); s.ev = append(s.ev, e); s.mu.Unlock() }

func (s *wireSeen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.ev {
		out = append(out, e.Kind+": "+e.Text)
	}
	return strings.Join(out, "\n")
}

// A call that waits behind another for the provider's one slot says so, and
// says when it got it; its request, the status it came back with and the
// first bytes of the answer follow. Before the wire, all of that was one
// long "waiting".
func TestACallSaysItIsQueuedAndWhatTheProviderAnswered(t *testing.T) {
	release := make(chan struct{})
	first := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		select {
		case first <- struct{}{}:
			<-release // the first call holds the only slot
		default:
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	}))
	t.Cleanup(srv.Close)
	p, err := NewPool(PoolConfig{Enabled: true, Strategy: StrategyRoundRobin, Providers: []Provider{
		{Name: "brain", BaseURL: srv.URL, Key: "k", ModelName: "m", MaxConcurrency: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []domain.Message{{Role: "user", Content: "hi"}}
	noop := func(*domain.GenerationResult) error { return nil }

	done := make(chan error, 1)
	go func() { done <- p.StreamWithTools(context.Background(), msgs, nil, nil, noop) }()
	<-first

	var seen wireSeen
	w := trace.NewWire(seen.on)
	second := make(chan error, 1)
	go func() { second <- p.StreamWithTools(trace.WithWire(context.Background(), w), msgs, nil, nil, noop) }()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(seen.text(), "queued") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	got := seen.text()
	for _, want := range []string{"queued: every provider is at its limit (brain 1/1)", "slot: got a slot after", "request: request sent (attempt 1): POST /chat/completions", "response: HTTP 200", "first_byte:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

// A provider that sends its headers and then nothing is named for what it
// is doing, not left as a call that is merely slow.
func TestAProviderThatAnsweredAndWentSilentIsNamed(t *testing.T) {
	tick, quiet := trace.WireTick, trace.WireQuiet
	trace.WireTick, trace.WireQuiet = 5*time.Millisecond, 20*time.Millisecond
	defer func() { trace.WireTick, trace.WireQuiet = tick, quiet }()
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	})
	var seen wireSeen
	w := trace.NewWire(seen.on)
	stop := make(chan struct{})
	go w.Heartbeat(context.Background(), trace.WireTick, trace.WireQuiet, stop)
	err := c.StreamWithTools(trace.WithWire(context.Background(), w), []domain.Message{{Role: "user", Content: "hi"}}, nil, nil,
		func(*domain.GenerationResult) error { return nil })
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	if got := seen.text(); !strings.Contains(got, "waiting: provider answered HTTP 200") || !strings.Contains(got, "first_byte") {
		t.Fatal(got)
	}
}

// A refusal comes back with its status, and the request sent again without
// the refused parameter says why it went again.
func TestARefusedParameterIsSaidAsARetry(t *testing.T) {
	n := 0
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"tool_choice is not supported in thinking mode"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	})
	var seen wireSeen
	ctx := trace.WithWire(context.Background(), trace.NewWire(seen.on))
	opts := &domain.GenerationOptions{ToolChoice: "required"}
	tools := []domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "f", Parameters: map[string]any{"type": "object"}}}}
	if err := c.StreamWithTools(ctx, []domain.Message{{Role: "user", Content: "hi"}}, tools, opts, func(*domain.GenerationResult) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got := seen.text()
	if !strings.Contains(got, "response: HTTP 400") || !strings.Contains(got, "retry: retrying: upstream refused a parameter") || !strings.Contains(got, "request sent (attempt 2)") {
		t.Fatal(got)
	}
}
