package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func sse(w http.ResponseWriter, chunks ...string) {
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
	}
}

func streamClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient("test", srv.URL, "k", "m")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func collect(t *testing.T, c *Client, opts *domain.GenerationOptions) (text, reasoning string, last *domain.GenerationResult, calls int) {
	t.Helper()
	err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, opts, func(r *domain.GenerationResult) error {
		calls++
		text += r.Content
		reasoning += r.ReasoningContent
		last = r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

// The point of the change: the first words reach the reader while the rest
// is still being written. The server holds the second chunk back until the
// first has been seen.
func TestTheFirstWordsArriveBeforeTheAnswerIsFinished(t *testing.T) {
	seen := make(chan struct{})
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, `{"choices":[{"delta":{"content":"成都"}}]}`)
		select {
		case <-seen:
		case <-time.After(3 * time.Second):
			t.Error("the first chunk was not delivered before the stream ended")
		}
		sse(w, `{"choices":[{"delta":{"content":"很好"},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	var once sync.Once
	var text string
	err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, nil, func(r *domain.GenerationResult) error {
		text += r.Content
		if r.Content == "成都" {
			once.Do(func() { close(seen) })
		}
		return nil
	})
	if err != nil || text != "成都很好" {
		t.Fatalf("text %q err %v", text, err)
	}
}

func TestAToolCallSplitAcrossChunksArrivesWhole(t *testing.T) {
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("stream not requested: %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"date\"}"}},{"index":1,"id":"call_2","function":{"name":"now","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":4}}}`,
			`[DONE]`)
	})
	_, _, last, _ := collect(t, c, nil)
	if len(last.ToolCalls) != 2 {
		t.Fatalf("tool calls %+v", last.ToolCalls)
	}
	tc := last.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "bash" || tc.Function.Arguments["command"] != "date" {
		t.Fatalf("first call %+v", tc)
	}
	if last.ToolCalls[1].Function.Name != "now" {
		t.Fatalf("second call %+v", last.ToolCalls[1])
	}
	if last.FinishReason != "tool_calls" {
		t.Fatalf("finish %q", last.FinishReason)
	}
	if last.Usage == nil || last.Usage.PromptTokens != 11 || last.Usage.CompletionTokens != 7 || last.Usage.CachedPromptTokens != 4 {
		t.Fatalf("usage %+v", last.Usage)
	}
}

func TestReasoningStreamsBesideTheAnswer(t *testing.T) {
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w,
			`{"choices":[{"delta":{"reasoning_content":"先想"}}]}`,
			`{"choices":[{"delta":{"reasoning":"一下"}}]}`,
			// A model that writes its thinking inline, with the tag cut
			// between two chunks.
			`{"choices":[{"delta":{"content":"<thi"}}]}`,
			`{"choices":[{"delta":{"content":"nk>再想</th"}}]}`,
			`{"choices":[{"delta":{"content":"ink>答案"}}]}`,
			`[DONE]`)
	})
	text, reasoning, _, calls := collect(t, c, nil)
	if text != "答案" || reasoning != "先想一下再想" {
		t.Fatalf("text %q reasoning %q", text, reasoning)
	}
	if calls < 3 {
		t.Fatalf("only %d callbacks: not streamed", calls)
	}
}

// glm-5.3 on DashScope answers 400 to enable_search; the retry drops it and
// streams.
func TestARefusedParameterIsDroppedAndTheStreamRetried(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if _, on := body["enable_search"]; on {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `data: {"error":{"code":"invalid_parameter_error","message":"This model does not support enable_search."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	text, _, _, _ := collect(t, c, &domain.GenerationOptions{WebSearchMode: domain.WebSearchModeAuto})
	if text != "ok" {
		t.Fatalf("text %q", text)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d requests", len(bodies))
	}
	if _, on := bodies[1]["enable_search"]; on {
		t.Fatal("the retry still asked for enable_search")
	}
}

// An upstream that ignores "stream" sends one JSON document.
func TestAnUpstreamThatWillNotStreamIsReadWhole(t *testing.T) {
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"whole","tool_calls":[{"id":"c","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	})
	text, _, last, _ := collect(t, c, nil)
	if text != "whole" || len(last.ToolCalls) != 1 || last.ToolCalls[0].Function.Arguments["command"] != "ls" || last.Usage == nil {
		t.Fatalf("text %q last %+v", text, last)
	}
}

func TestAnErrorInTheStreamIsAnError(t *testing.T) {
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, `{"choices":[{"delta":{"content":"half"}}]}`, `{"error":{"message":"upstream overloaded"}}`)
	})
	err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, nil, nil, func(*domain.GenerationResult) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "upstream overloaded") {
		t.Fatalf("err %v", err)
	}
}

func TestThinkSplitterHoldsOnlyWhatCouldBeATag(t *testing.T) {
	var s thinkSplitter
	text, think := s.feed("a <b> c <")
	if text != "a <b> c " || think != "" {
		t.Fatalf("%q %q", text, think)
	}
	text, _ = s.feed("x")
	if text != "<x" {
		t.Fatalf("a held '<' that was not a tag was lost: %q", text)
	}
}

// A model that keeps reasoning past the budget without saying anything is
// asked once more with thinking off, and that answer is the one kept.
func TestAModelThatWillNotStopThinkingIsAskedAgainWithoutIt(t *testing.T) {
	var mu sync.Mutex
	var thinking []any
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		thinking = append(thinking, body["enable_thinking"])
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if body["enable_thinking"] == false {
			sse(w, `{"choices":[{"delta":{"content":"hostname-1"},"finish_reason":"stop"}]}`, `[DONE]`)
			return
		}
		for i := 0; i < 200; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			sse(w, `{"choices":[{"delta":{"reasoning_content":"still calibrating the clock… "}}]}`)
		}
	})
	c.SetReasoningBudget(200, -1)
	text, reasoning, _, _ := collect(t, c, nil)
	if text != "hostname-1" {
		t.Fatalf("text %q", text)
	}
	if reasoning == "" || len(reasoning) > 400 {
		t.Fatalf("reasoning kept %d bytes", len(reasoning))
	}
	if len(thinking) != 2 || thinking[0] != nil || thinking[1] != false {
		t.Fatalf("requests %v", thinking)
	}
}

func TestAModelThatThinksBrieflyIsLeftAlone(t *testing.T) {
	calls := 0
	c := streamClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, `{"choices":[{"delta":{"reasoning_content":"short"}}]}`,
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	c.SetReasoningBudget(200, -1)
	text, _, _, _ := collect(t, c, nil)
	if text != "ok" || calls != 1 {
		t.Fatalf("text %q calls %d", text, calls)
	}
}
