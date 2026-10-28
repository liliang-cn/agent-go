package pool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// anthropicClient is a client for https://api.anthropic.com/v1 whose
// requests land on a local server instead, so the whole path — the pool
// building a chat-completions request, the transport translating it, the
// pool parsing the translated answer — runs as it does for real.
func anthropicClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient("anthropic", "https://api.anthropic.com/v1", "sk-test", "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	local, _ := url.Parse(srv.URL)
	c.http.Transport = wireTransport{base: anthropicTransport{base: redirect{to: local}}}
	return c
}

type redirect struct{ to *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestIsAnthropicNative(t *testing.T) {
	for u, want := range map[string]bool{
		"https://api.anthropic.com/v1":  true,
		"https://API.anthropic.com/v1/": true,
		"https://openrouter.ai/api/v1":  false,
		"http://127.0.0.1:43517/v1":     false,
	} {
		if got := isAnthropicNative(u); got != want {
			t.Errorf("isAnthropicNative(%q) = %v", u, got)
		}
	}
}

func TestChatToMessagesShapesARequestTheMessagesAPITakes(t *testing.T) {
	in := `{
	  "model": "claude-opus-5-5", "max_tokens": 4096, "temperature": 0.2, "stream": true,
	  "reasoning_effort": "high", "tool_choice": "required", "parallel_tool_calls": false,
	  "tools": [
	    {"type":"function","function":{"name":"a","description":"first","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}},
	    {"type":"function","function":{"name":"b"}}
	  ],
	  "messages": [
	    {"role":"system","content":"be brief"},
	    {"role":"user","content":"look it up"},
	    {"role":"assistant","content":"","tool_calls":[
	      {"id":"t1","type":"function","function":{"name":"a","arguments":"{\"x\":\"1\"}"}},
	      {"id":"t2","type":"function","function":{"name":"b","arguments":""}}]},
	    {"role":"tool","tool_call_id":"t1","content":"one"},
	    {"role":"tool","tool_call_id":"t2","content":""},
	    {"role":"user","content":"Analyze the tool results above."},
	    {"role":"system","content":"late rule"}
	  ]}`
	out, stream, err := chatToMessages([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !stream {
		t.Fatal("stream not reported")
	}
	var got struct {
		Model        string                       `json:"model"`
		MaxTokens    int                          `json:"max_tokens"`
		Temperature  *float64                     `json:"temperature"`
		System       []map[string]interface{}     `json:"system"`
		Tools        []map[string]interface{}     `json:"tools"`
		ToolChoice   map[string]interface{}       `json:"tool_choice"`
		OutputConfig map[string]interface{}       `json:"output_config"`
		CacheControl map[string]interface{}       `json:"cache_control"`
		Messages     []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-opus-5-5" || got.MaxTokens != 4096 || got.Temperature != nil {
		t.Fatalf("model/max_tokens/temperature wrong: %s", out)
	}
	if len(got.System) != 2 || got.System[0]["text"] != "be brief" || got.System[1]["text"] != "late rule" {
		t.Fatalf("system = %v", got.System)
	}
	if len(got.Tools) != 2 || got.Tools[1]["cache_control"] == nil || got.Tools[0]["cache_control"] != nil {
		t.Fatalf("the last tool, and only it, carries the prefix mark: %v", got.Tools)
	}
	if got.ToolChoice["type"] != "auto" || got.ToolChoice["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %v", got.ToolChoice)
	}
	if got.OutputConfig["effort"] != "high" || got.CacheControl["type"] != "ephemeral" {
		t.Fatalf("output_config %v cache_control %v", got.OutputConfig, got.CacheControl)
	}
	// user, assistant(tool_use x2), user(tool_result x2 + text)
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %s", out)
	}
	var roles []string
	for _, m := range got.Messages {
		var r string
		_ = json.Unmarshal(m["role"], &r)
		roles = append(roles, r)
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("roles = %v", roles)
	}
	var last []map[string]interface{}
	_ = json.Unmarshal(got.Messages[2]["content"], &last)
	if len(last) != 3 || last[0]["type"] != "tool_result" || last[0]["tool_use_id"] != "t1" || last[1]["content"] != "(no output)" || last[2]["type"] != "text" {
		t.Fatalf("tool results and the following text share one user turn: %v", last)
	}
	var calls []map[string]interface{}
	_ = json.Unmarshal(got.Messages[1]["content"], &calls)
	if len(calls) != 2 || calls[0]["type"] != "tool_use" || calls[0]["input"].(map[string]interface{})["x"] != "1" {
		t.Fatalf("tool_use blocks = %v", calls)
	}
}

func TestGenerateWithToolsOverMessagesAPI(t *testing.T) {
	var sent map[string]interface{}
	var headers http.Header
	c := anthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s, want /v1/messages", r.URL.Path)
		}
		headers = r.Header
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","model":"claude-opus-5-5","stop_reason":"tool_use",
		  "content":[{"type":"thinking","thinking":""},{"type":"text","text":"checking"},
		    {"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}],
		  "usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":900,"cache_creation_input_tokens":100}}`)
	})
	res, err := c.GenerateWithTools(context.Background(),
		[]domain.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}},
		[]domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "lookup", Parameters: map[string]interface{}{"type": "object"}}}},
		&domain.GenerationOptions{MaxTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("x-api-key") != "sk-test" || headers.Get("Authorization") != "" || headers.Get("anthropic-version") == "" {
		t.Fatalf("auth headers = %v", headers)
	}
	if sent["system"] == nil || sent["cache_control"] == nil {
		t.Fatalf("request = %v", sent)
	}
	if res.Content != "checking" || len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "toolu_1" || res.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage == nil || res.Usage.PromptTokens != 1010 || res.Usage.CachedPromptTokens != 900 || res.Usage.CacheWriteTokens != 100 || res.Usage.CompletionTokens != 5 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestStreamWithToolsOverMessagesAPI(t *testing.T) {
	c := anthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_2","model":"claude-opus-5-5","usage":{"input_tokens":7,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"lookup","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "event: x\ndata: "+ev+"\n\n")
			w.(http.Flusher).Flush()
		}
	})
	var text string
	var last *domain.GenerationResult
	err := c.StreamWithTools(context.Background(), []domain.Message{{Role: "user", Content: "hi"}},
		[]domain.ToolDefinition{{Type: "function", Function: domain.ToolFunction{Name: "lookup"}}},
		&domain.GenerationOptions{}, func(r *domain.GenerationResult) error {
			text += r.Content
			last = r
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello") {
		t.Fatalf("text = %q", text)
	}
	if last == nil || len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "toolu_9" {
		t.Fatalf("last = %+v", last)
	}
	args, _ := json.Marshal(last.ToolCalls[0].Function.Arguments)
	if !strings.Contains(string(args), `"q":"x"`) {
		t.Fatalf("arguments = %s", args)
	}
	if last.Usage == nil || last.Usage.CachedPromptTokens != 2000 || last.Usage.CompletionTokens != 12 || last.Usage.PromptTokens != 2007 {
		t.Fatalf("usage = %+v", last.Usage)
	}
}

// A tool called with no arguments comes back from history as "null" (a nil
// map marshalled), and the Messages API refuses any tool_use input that is
// not an object: one such call fails every later turn of the conversation.
func TestAToolUseInputIsAlwaysAnObject(t *testing.T) {
	for _, args := range []string{``, `null`, `""`, `[]`, `"x"`, `3`, `not json`, `{}`, `{"a":1}`} {
		raw, _ := json.Marshal(args)
		in := `{"model":"claude-opus-5-5","messages":[
		  {"role":"user","content":"go"},
		  {"role":"assistant","content":"","tool_calls":[{"id":"t1","type":"function","function":{"name":"status","arguments":` + string(raw) + `}}]},
		  {"role":"tool","tool_call_id":"t1","content":"ok"}]}`
		out, _, err := chatToMessages([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", args, err)
		}
		var got struct {
			Messages []struct {
				Content []struct {
					Type  string          `json:"type"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		var input json.RawMessage
		for _, m := range got.Messages {
			for _, c := range m.Content {
				if c.Type == "tool_use" {
					input = c.Input
				}
			}
		}
		var obj map[string]any
		if json.Unmarshal(input, &obj) != nil || obj == nil {
			t.Errorf("arguments %q became input %s; want an object", args, input)
		}
	}
}
