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

// A search the model ran, the page it found, and an answer citing it: what a
// Messages response with the web search tool looks like.
const searchedContent = `[
  {"type":"text","text":"Let me look."},
  {"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"spacex news"}},
  {"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
    {"type":"web_search_result","url":"https://example.org/a","title":"A","encrypted_content":"enc","page_age":null}]},
  {"type":"text","text":"Starship flew.","citations":[
    {"type":"web_search_result_location","url":"https://example.org/a","title":"A","encrypted_index":"ix","cited_text":"flew"}]}
]`

func anthropicSearchClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	c := anthropicClient(t, h)
	if err := c.SetNativeWebSearch(domain.NativeWebSearchAnthropic, map[string]interface{}{"max_uses": 3}); err != nil {
		t.Fatal(err)
	}
	return c
}

func readBody(r *http.Request) map[string]any {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return body
}

// The server tool goes beside the function tools in one request; the
// function tools keep the cache breakpoint; the searched answer comes back
// as its text, and the citations prove the model searched.
func TestAnthropicWebSearchToolSentBesideFunctionTools(t *testing.T) {
	var sent map[string]any
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		sent = readBody(r)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","model":"claude-opus-5-5","stop_reason":"end_turn","content":`+searchedContent+`,
		  "usage":{"input_tokens":10,"output_tokens":5}}`)
	})
	tools := []domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "lookup", Parameters: map[string]interface{}{"type": "object"}}}}
	res, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "news?"}}, tools, autoOpts())
	if err != nil {
		t.Fatal(err)
	}
	list, _ := sent["tools"].([]any)
	if len(list) != 2 {
		t.Fatalf("tools = %v, want the function tool and web_search", sent["tools"])
	}
	fn, _ := list[0].(map[string]any)
	srv, _ := list[1].(map[string]any)
	if fn["name"] != "lookup" || fn["cache_control"] == nil {
		t.Fatalf("function tool = %v", fn)
	}
	if srv["type"] != domain.AnthropicWebSearchFiltering || srv["name"] != "web_search" || srv["max_uses"] != float64(3) || srv["input_schema"] != nil {
		t.Fatalf("server tool = %v", srv)
	}
	if res.Content != "Let me look.Starship flew." || len(res.ToolCalls) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !c.NativeWebSearchProven() {
		t.Fatal("a cited search did not prove native search")
	}
}

// The search blocks are not carried into history: the next turn sends the
// answer as plain assistant text, which the API takes, and never a
// server_tool_use without its result.
func TestAnthropicSearchTurnReplaysAsText(t *testing.T) {
	var second map[string]any
	n := 0
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		body := readBody(r)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			io.WriteString(w, `{"id":"m1","stop_reason":"end_turn","content":`+searchedContent+`,"usage":{}}`)
			return
		}
		second = body
		io.WriteString(w, `{"id":"m2","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{}}`)
	})
	msgs := []domain.Message{{Role: "user", Content: "news?"}}
	res, err := c.GenerateWithTools(context.Background(), msgs, nil, autoOpts())
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, domain.Message{Role: "assistant", Content: res.Content}, domain.Message{Role: "user", Content: "more?"})
	if _, err := c.GenerateWithTools(context.Background(), msgs, nil, autoOpts()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(second["messages"])
	if strings.Contains(string(raw), "server_tool_use") || strings.Contains(string(raw), "web_search_tool_result") {
		t.Fatalf("history carries search blocks: %s", raw)
	}
	if !strings.Contains(string(raw), "Starship flew.") {
		t.Fatalf("history lost the answer: %s", raw)
	}
}

// pause_turn: the server's loop stopped mid-search. The transport sends the
// turn back as the assistant's, verbatim, and the caller gets one answer
// with the usage of both requests.
func TestAnthropicPauseTurnIsResumed(t *testing.T) {
	var bodies []map[string]any
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, readBody(r))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			io.WriteString(w, `{"id":"m1","stop_reason":"pause_turn","content":[
			  {"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}}],
			  "usage":{"input_tokens":100,"output_tokens":10}}`)
			return
		}
		io.WriteString(w, `{"id":"m2","stop_reason":"end_turn","content":[
		  {"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.org/b","title":"B"}]},
		  {"type":"text","text":"found it"}],
		  "usage":{"input_tokens":120,"output_tokens":20}}`)
	})
	res, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "q"}}, nil, autoOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the paused one and its resume", len(bodies))
	}
	msgs, _ := bodies[1]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := last["content"].([]any)
	if last["role"] != "assistant" || len(content) != 1 || content[0].(map[string]any)["type"] != "server_tool_use" {
		t.Fatalf("resume did not send the paused turn back: %v", last)
	}
	if res.Content != "found it" || res.Usage == nil || res.Usage.PromptTokens != 220 || res.Usage.CompletionTokens != 30 {
		t.Fatalf("result = %+v usage = %+v", res, res.Usage)
	}
	if !c.NativeWebSearchProven() {
		t.Fatal("search results did not prove native search")
	}
}

func messagesSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range events {
		io.WriteString(w, "event: x\ndata: "+ev+"\n\n")
		w.(http.Flusher).Flush()
	}
}

// A streamed search: the search blocks stream past without becoming text or
// tool calls, citations prove the search, and a paused stream is resumed
// with its blocks rebuilt — the streamed query and the citations included.
func TestAnthropicStreamedSearchWithPauseTurn(t *testing.T) {
	var bodies []map[string]any
	c := anthropicSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, readBody(r))
		if len(bodies) == 1 {
			messagesSSE(w,
				`{"type":"message_start","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":50,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"q\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.org/a","title":"A","encrypted_content":"enc"}]}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"","citations":null}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.org/a","title":"A","encrypted_index":"ix","cited_text":"x"}}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Part one. "}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":9}}`,
				`{"type":"message_stop"}`,
			)
			return
		}
		messagesSSE(w,
			`{"type":"message_start","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":70,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Part two."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		)
	})
	var text string
	var last *domain.GenerationResult
	err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "q"}}, nil, autoOpts(),
		func(r *domain.GenerationResult) error {
			text += r.Content
			last = r
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Part one. Part two.") {
		t.Fatalf("text = %q", text)
	}
	if last == nil || len(last.ToolCalls) != 0 {
		t.Fatalf("last = %+v", last)
	}
	if last.Usage == nil || last.Usage.PromptTokens != 120 || last.Usage.CompletionTokens != 13 {
		t.Fatalf("usage = %+v", last.Usage)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the paused stream and its resume", len(bodies))
	}
	msgs, _ := bodies[1]["messages"].([]any)
	turn, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := turn["content"].([]any)
	if turn["role"] != "assistant" || len(blocks) != 3 {
		t.Fatalf("resumed turn = %v", turn)
	}
	use := blocks[0].(map[string]any)
	if use["type"] != "server_tool_use" || use["input"].(map[string]any)["query"] != "q" {
		t.Fatalf("server_tool_use rebuilt as %v", use)
	}
	if blocks[1].(map[string]any)["type"] != "web_search_tool_result" {
		t.Fatalf("result block rebuilt as %v", blocks[1])
	}
	txt := blocks[2].(map[string]any)
	cites, _ := txt["citations"].([]any)
	if txt["text"] != "Part one. " || len(cites) != 1 || cites[0].(map[string]any)["encrypted_index"] != "ix" {
		t.Fatalf("text block rebuilt as %v", txt)
	}
	if !c.NativeWebSearchProven() {
		t.Fatal("streamed citations did not prove native search")
	}
}

// A function tool already named web_search keeps the name; the server tool
// steps aside rather than make the request one the API refuses.
func TestAnthropicServerToolStepsAsideForAFunctionToolOfItsName(t *testing.T) {
	out, _, err := chatToMessages([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[
	  {"type":"function","function":{"name":"web_search","parameters":{"type":"object"}}},
	  {"type":"web_search_20260209","name":"web_search"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(out, &req)
	if len(req.Tools) != 1 || req.Tools[0]["input_schema"] == nil {
		t.Fatalf("tools = %v, want the function tool only", req.Tools)
	}
}

// Declared on an endpoint that is not the Messages API, the server tool is
// refused the way google_search is: the round degrades to no native search
// instead of failing, and the declaration falls back to unsupported.
func TestAnthropicSearchRefusedByAChatCompletionsEndpoint(t *testing.T) {
	var bodies []map[string]any
	c := nativeSearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		bodies = append(bodies, body)
		for _, tl := range body["tools"].([]any) {
			if tl.(map[string]any)["type"] == domain.AnthropicWebSearchFiltering {
				http.Error(w, `{"error":{"message":"Invalid value: 'web_search_20260209'. Supported values are: 'function' and 'custom'.","param":"tools[1].type"}}`, http.StatusBadRequest)
				return
			}
		}
		_, _ = w.Write([]byte(plainCompletion("ok")))
	})
	if err := c.SetNativeWebSearch(domain.NativeWebSearchAnthropic); err != nil {
		t.Fatal(err)
	}
	tools := []domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "fs_read"}}}
	if _, err := c.GenerateWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}}, tools, autoOpts()); err != nil {
		t.Fatalf("round failed instead of degrading: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the rejected one and a retry", len(bodies))
	}
	if retried, _ := bodies[1]["tools"].([]any); len(retried) != 1 {
		t.Fatalf("retry tools = %v, want only the function tool", bodies[1]["tools"])
	}
	if s, k := c.NativeWebSearchVerdict(); s || !k {
		t.Fatalf("verdict after rejection = (%v, %v), want (false, true)", s, k)
	}
}

func TestDeclaredAnthropicSendsOnlyItsTool(t *testing.T) {
	req := buildPoolGenerateWithToolsRequest("m", nil, nil, autoOpts(), domain.NativeWebSearchAnthropic,
		map[string]interface{}{"allowed_domains": []string{"example.org"}, "type": domain.AnthropicWebSearchBasic})
	if _, has := req["web_search_options"]; has {
		t.Fatal("web_search_options sent")
	}
	if _, has := req["enable_search"]; has {
		t.Fatal("enable_search sent")
	}
	list, _ := req["tools"].([]map[string]interface{})
	if len(list) != 1 || list[0]["type"] != domain.AnthropicWebSearchBasic || list[0]["name"] != "web_search" || list[0]["allowed_domains"] == nil {
		t.Fatalf("tools = %v", list)
	}
	if plain := buildPoolGenerateWithToolsRequest("m", nil, nil, &domain.GenerationOptions{}, domain.NativeWebSearchAnthropic); plain["tools"] != nil {
		t.Fatal("web_search sent on a request that did not ask for native search")
	}
}
