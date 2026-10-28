package pool

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// decisionSink records what pkg/trace hands a runtime sink.
type decisionSink struct {
	endSink
	mu        sync.Mutex
	decisions []trace.Decision
}

func (s *decisionSink) Decision(_ context.Context, d trace.Decision) {
	s.mu.Lock()
	s.decisions = append(s.decisions, d)
	s.mu.Unlock()
}

func (s *decisionSink) whats() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, d := range s.decisions {
		out = append(out, d.What)
	}
	return out
}

// A turn that searched twice, was paused once, and cited what it found:
// none of it was visible past the transport. It now rides back on the
// result, use by use.
func TestAnthropicServerToolReportWhole(t *testing.T) {
	n := 0
	var sentTraceparent string
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		sentTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			io.WriteString(w, `{"id":"m1","stop_reason":"pause_turn","content":[
			  {"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"first"}},
			  {"type":"web_search_tool_result","tool_use_id":"s1","content":[
			    {"type":"web_search_result","url":"https://a.example/1","title":"1"},
			    {"type":"web_search_result","url":"https://a.example/2","title":"2"}]},
			  {"type":"server_tool_use","id":"s2","name":"web_search","input":{"query":"second"}}],
			  "usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":7}}`)
			return
		}
		io.WriteString(w, `{"id":"m2","stop_reason":"end_turn","content":[
		  {"type":"web_search_tool_result","tool_use_id":"s2","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}},
		  {"type":"text","text":"done","citations":[{"type":"web_search_result_location","url":"https://a.example/1"},{"type":"web_search_result_location","url":"https://a.example/2"}]}],
		  "usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":5}}`)
	})
	ctx, sc := trace.StartSpan(context.Background())
	res, err := c.GenerateWithTools(ctx, []domain.Message{{Role: "user", Content: "q"}}, nil, autoOpts())
	if err != nil {
		t.Fatal(err)
	}
	rep := res.ServerTools
	if rep == nil || len(rep.Uses) != 2 || rep.Resumes != 1 || len(rep.RequestMs) != 2 || rep.Citations != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if u := rep.Uses[0]; u.Name != "web_search" || u.Input["query"] != "first" || u.Results != 2 || len(u.URLs) != 2 {
		t.Fatalf("first use = %+v", u)
	}
	if u := rep.Uses[1]; u.Input["query"] != "second" || u.ErrorCode != "max_uses_exceeded" {
		t.Fatalf("second use = %+v", u)
	}
	if res.Usage == nil || res.Usage.CacheWriteTokens != 7 || res.Usage.CachedPromptTokens != 5 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.Streamed {
		t.Fatal("a whole answer reported as streamed")
	}
	if sentTraceparent != sc.Traceparent() {
		t.Fatalf("traceparent sent %q, want %q", sentTraceparent, sc.Traceparent())
	}
}

func TestAnthropicServerToolReportStreamed(t *testing.T) {
	n := 0
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			messagesSSE(w,
				`{"type":"message_start","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":50,"output_tokens":1,"cache_creation_input_tokens":40}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://example.org/a","title":"A"}]}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":9}}`,
				`{"type":"message_stop"}`,
			)
			return
		}
		messagesSSE(w,
			`{"type":"message_start","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":70,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.org/a","title":"A"}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		)
	})
	var last *domain.GenerationResult
	if err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "q"}}, nil, autoOpts(),
		func(r *domain.GenerationResult) error { last = r; return nil }); err != nil {
		t.Fatal(err)
	}
	rep := last.ServerTools
	if rep == nil || len(rep.Uses) != 1 || rep.Uses[0].Input["query"] != "q" || rep.Uses[0].Results != 1 || rep.Resumes != 1 || len(rep.RequestMs) != 2 || rep.Citations != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if !last.Streamed || last.Usage.CacheWriteTokens != 40 {
		t.Fatalf("streamed=%v usage=%+v", last.Streamed, last.Usage)
	}
}

// Dropping the server tool, and retrying a round without native search, are
// choices about what the model is offered; each is a decision on the trace.
func TestAnthropicDecisionsReachTheSink(t *testing.T) {
	sink := &decisionSink{}
	ctx := trace.WithSink(context.Background(), sink)
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{}}`)
	})
	tools := []domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "web_search", Parameters: map[string]interface{}{"type": "object"}}}}
	if _, err := c.GenerateWithTools(ctx, []domain.Message{{Role: "user", Content: "q"}}, tools, autoOpts()); err != nil {
		t.Fatal(err)
	}
	if w := sink.whats(); len(w) != 1 || w[0] != "drop_server_tool" {
		t.Fatalf("decisions = %v", w)
	}

	sink2 := &decisionSink{}
	ctx2 := trace.WithSink(context.Background(), sink2)
	rc := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if readBody(r)["web_search_options"] != nil {
			http.Error(w, `{"error":{"message":"unsupported parameter web_search_options"}}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(plainCompletion("ok")))
	})
	if _, err := rc.GenerateWithTools(ctx2, []domain.Message{{Role: "user", Content: "q"}}, nil, autoOpts()); err != nil {
		t.Fatal(err)
	}
	if w := sink2.whats(); len(w) != 1 || w[0] != "retry_without_native_search" {
		t.Fatalf("decisions = %v", w)
	}
}
