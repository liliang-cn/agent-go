package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

type endSink struct {
	mu   sync.Mutex
	ends []trace.Record
	wire []trace.WireEvent
}

func (s *endSink) ModelStart(context.Context, string, string) {}
func (s *endSink) ModelEnd(_ context.Context, _ string, r trace.Record) {
	s.mu.Lock()
	s.ends = append(s.ends, r)
	s.mu.Unlock()
}
func (s *endSink) Step(context.Context, trace.Record) {}
func (s *endSink) Wire(_ context.Context, _, _ string, e trace.WireEvent) {
	s.mu.Lock()
	s.wire = append(s.wire, e)
	s.mu.Unlock()
}

// The calls a run makes on its own behalf — constraint extraction, a
// summary, the memory pick — go through Generate and GenerateStructured,
// which return no token accounting, so their traces said 0 tokens and 0 to
// first byte: the cost and the wait of every one of them was invisible. The
// provider reports both; the wire now carries them to the call's record.
//
// The wait is the call's duration, not a time to first token: the answer came
// as one JSON document, so its first byte was its last, and a ttft equal to
// the duration said nothing the duration did not.
func TestAnAuxiliaryCallRecordsItsTokensAndFirstByte(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1200,"completion_tokens":34,"prompt_tokens_details":{"cached_tokens":800}}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient("p", srv.URL, "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	sink := &endSink{}
	ctx := trace.WithSink(context.Background(), sink)
	call := trace.Model(ctx, "constraints")
	if _, err := c.GenerateStructured(call.Context(ctx), "x", map[string]any{"type": "object"}, nil); err != nil {
		t.Fatal(err)
	}
	call.End(nil, nil)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ends) != 1 {
		t.Fatalf("%d ends", len(sink.ends))
	}
	r := sink.ends[0]
	if r.In != 1200 || r.Out != 34 || r.Cached != 800 {
		t.Errorf("tokens in=%d out=%d cached=%d, want 1200/34/800", r.In, r.Out, r.Cached)
	}
	if r.Streamed || r.TTFTMs != 0 {
		t.Errorf("a whole answer reported streamed=%v ttft=%dms", r.Streamed, r.TTFTMs)
	}
	if r.DurMs < 25 {
		t.Errorf("duration = %dms, want the wait", r.DurMs)
	}
}

// Over the Messages API the wire names the request that actually went out,
// /v1/messages at its translated size, not the chat-completions request it
// was translated from.
func TestTheWireNamesTheMessagesRequest(t *testing.T) {
	var path string
	c := anthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`))
	})
	sink := &endSink{}
	ctx := trace.WithSink(context.Background(), sink)
	call := trace.Model(ctx, "turn")
	if _, err := c.Generate(call.Context(ctx), "hello", nil); err != nil {
		t.Fatal(err)
	}
	call.End(nil, nil)
	if path != "/v1/messages" {
		t.Fatalf("upstream got %s", path)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var req string
	for _, e := range sink.wire {
		if e.Kind == "request" {
			req = e.Text
		}
	}
	if !strings.Contains(req, "POST /v1/messages") {
		t.Errorf("wire request = %q, want POST /v1/messages", req)
	}
	if sink.ends[0].In != 5 || sink.ends[0].Out != 1 {
		t.Errorf("tokens = %+v", sink.ends[0])
	}
}

// The wire sits inside the translation, so what it reports is what was sent.
func TestTheWireSitsInsideTheMessagesTranslation(t *testing.T) {
	c, err := NewClient("p", "https://api.anthropic.com/v1", "k", "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	at, ok := c.http.Transport.(anthropicTransport)
	if !ok {
		t.Fatalf("outer transport = %T, want anthropicTransport", c.http.Transport)
	}
	if _, ok := at.base.(wireTransport); !ok {
		t.Fatalf("inside the translation = %T, want wireTransport", at.base)
	}
}
