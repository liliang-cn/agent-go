package pool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range events {
		io.WriteString(w, "event: x\ndata: "+ev+"\n\n")
		w.(http.Flusher).Flush()
	}
}

// fakeAnthropic plays a question that needs the web: the first turn searches
// twice across a pause_turn and asks for a function tool, the second answers.
// Structured calls (constraint extraction) get a JSON object.
func fakeAnthropic(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	turns := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if r.Header.Get("x-api-key") != "sk-ant-api03-testkeytestkeytestkey" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		if body["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"m_c","model":"claude-opus-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"{}"}],
			  "usage":{"input_tokens":900,"output_tokens":12}}`)
			return
		}
		msgs, _ := body["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		mu.Lock()
		turns++
		n := turns
		mu.Unlock()
		switch {
		case last["role"] == "assistant": // the resume of a paused turn
			sse(w,
				`{"type":"message_start","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":2100,"output_tokens":1,"cache_read_input_tokens":114000}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s2","name":"web_search","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"starship flight 12 date\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"s2","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://news.example/starship","title":"Starship"}}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Checking the launch window. "}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`,
				`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"topic\":\"starship\"}"}}`,
				`{"type":"content_block_stop","index":3}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":160}}`,
				`{"type":"message_stop"}`)
		case n == 1:
			sse(w,
				`{"type":"message_start","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":1800,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":112000}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"spacex starship latest launch\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"s1","content":[`+
					`{"type":"web_search_result","url":"https://news.example/starship","title":"Starship"},`+
					`{"type":"web_search_result","url":"https://news.example/spacex","title":"SpaceX"},`+
					`{"type":"web_search_result","url":"https://news.example/faa","title":"FAA"}]}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":40}}`,
				`{"type":"message_stop"}`)
		default:
			sse(w,
				`{"type":"message_start","message":{"id":"m3","model":"claude-opus-5-5","usage":{"input_tokens":400,"output_tokens":1,"cache_read_input_tokens":116000,"cache_creation_input_tokens":2300}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Starship flew on its last attempt; "}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the next window is not yet set."}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30}}`,
				`{"type":"message_stop"}`)
		}
	}))
}

func agentConfig(home string) *config.Config {
	cfg := &config.Config{Home: home, RAG: config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{StoreType: "file", MemoryPath: filepath.Join(home, "data", "memories")}}
	cfg.ApplyHomeLayout()
	return cfg
}

// A run on the Messages API, end to end through the real client: the
// server-side searches, the pause_turn resume, the cache write and the
// function tool all reach the trace and the log, joined by one trace id, and
// the API key reaches neither.
func TestARealLookingRunTracesAndLogsEndToEnd(t *testing.T) {
	traceOut, infoLog := runStarship(t, slog.LevelInfo)
	_, debugLog := runStarship(t, slog.LevelDebug)

	var turn1 map[string]any
	for _, l := range strings.Split(strings.TrimSpace(traceOut), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q", l)
		}
		if m["trace_id"] != "0af7651916cd43dd8448eb211c80319c" {
			t.Fatalf("line outside the flow: %s", l)
		}
		if m["event"] == "model_end" && m["purpose"] == "turn" && turn1 == nil {
			turn1 = m
		}
	}
	st, _ := turn1["server_tools"].(map[string]any)
	tok, _ := turn1["tokens"].(map[string]any)
	if st == nil || len(st["uses"].([]any)) != 2 || st["resumes"].(float64) != 1 || len(st["request_ms"].([]any)) != 2 || st["citations"].(float64) != 1 ||
		tok["cache_write"].(float64) != 112000 || turn1["streamed"] != true {
		t.Fatalf("first turn = %v", turn1)
	}
	if use := st["uses"].([]any)[0].(map[string]any); use["results"].(float64) != 3 {
		t.Fatalf("first search = %v", use)
	}
	if strings.Contains(traceOut+infoLog+debugLog, "testkeytestkey") {
		t.Fatal("the API key reached a trace or a log")
	}
	for _, want := range []string{`msg="run start"`, `msg="model call"`, `server_tools="web_search×2`, `msg="tool call"`, "tool=lookup", `msg="run end"`, "trace_id=0af7651916cd43dd8448eb211c80319c"} {
		if !strings.Contains(infoLog, want) {
			t.Fatalf("info log lacks %q:\n%s", want, infoLog)
		}
	}
	if strings.Contains(infoLog, "level=DEBUG") || strings.Count(infoLog, "trace_id=") != strings.Count(infoLog, "\n") {
		t.Fatalf("info log: debug lines or a line without exactly one trace id:\n%s", infoLog)
	}
	for _, want := range []string{`msg="model wire"`, `msg="tool start"`} {
		if !strings.Contains(debugLog, want) {
			t.Fatalf("debug log lacks %q:\n%s", want, debugLog)
		}
	}
	if dir := os.Getenv("AGENTGO_SAMPLE_OUT"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "run.jsonl"), []byte(traceOut), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "info.log"), []byte(infoLog), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "debug.log"), []byte(debugLog), 0o600)
	}
}

func runStarship(t *testing.T, level slog.Level) (string, string) {
	t.Helper()
	srv := fakeAnthropic(t)
	defer srv.Close()
	client, err := pool.NewAnthropicTestClient(srv.URL, "sk-ant-api03-testkeytestkeytestkey", "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetNativeWebSearch(domain.NativeWebSearchAnthropic); err != nil {
		t.Fatal(err)
	}
	traceOut, logOut := &lockedBuffer{}, &lockedBuffer{}
	svc, err := agent.New("researcher").WithConfig(agentConfig(t.TempDir())).WithLLM(client).
		WithObserver(agent.NewTraceWriter(traceOut)).WithLogger(agentgolog.New(logOut, level, "text")).
		WithTool(agent.NewTool("lookup", "Look a topic up in the notes", func(_ context.Context, p *struct {
			Topic string `json:"topic"`
		}) (any, error) {
			return map[string]any{"notes": "flight 11 landed in the Indian Ocean"}, nil
		})).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := trace.WithTraceparent(context.Background(), "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	res, err := svc.Run(ctx, "What happened with SpaceX Starship this week?", agent.WithRunID("run-starship"))
	if err != nil || res == nil || !strings.Contains(res.Text(), "Starship flew") {
		t.Fatalf("run: %v %+v", err, res)
	}
	return traceOut.String(), logOut.String()
}

