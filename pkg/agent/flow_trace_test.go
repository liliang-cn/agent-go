package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// flowLLM answers a parent run (call "spawn", then finish) and the sub-agent
// that tool starts (answer at once), told apart by the sub-agent's goal.
type flowLLM struct {
	mu     sync.Mutex
	parent int
}

func (l *flowLLM) turn(msgs []domain.Message) *domain.GenerationResult {
	for _, m := range msgs {
		if strings.Contains(m.Content, "SUBTASK") {
			return &domain.GenerationResult{Content: "sub done", Streamed: true,
				Usage: &domain.TokenUsage{PromptTokens: 30, CompletionTokens: 3}}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.parent++
	if l.parent == 1 {
		return &domain.GenerationResult{Streamed: true, ToolCalls: []domain.ToolCall{{ID: "call_spawn", Type: "function",
			Function: domain.FunctionCall{Name: "spawn", Arguments: map[string]any{"api_key": "sk-ant-api03-supersecretvalue0123456789"}}}},
			Usage: &domain.TokenUsage{PromptTokens: 100, CompletionTokens: 10, CachedPromptTokens: 60, CacheWriteTokens: 40},
			ServerTools: &domain.ServerToolReport{Uses: []domain.ServerToolUse{
				{Name: "web_search", Input: map[string]any{"query": "spacex news"}, Results: 5, URLs: []string{"https://example.org/a"}},
				{Name: "web_search", Input: map[string]any{"query": "starship"}, ErrorCode: "max_uses_exceeded"}},
				Resumes: 1, RequestMs: []int64{1200, 800}, Citations: 4}}
	}
	return &domain.GenerationResult{Content: "all done", Streamed: true, Usage: &domain.TokenUsage{PromptTokens: 150, CompletionTokens: 5, CachedPromptTokens: 100}}
}

func (l *flowLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *flowLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *flowLLM) GenerateWithTools(_ context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.turn(m), nil
}
func (l *flowLLM) StreamWithTools(ctx context.Context, m []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	trace.Decide(ctx, "drop_server_tool", "a function tool already has the server tool's name", map[string]any{"tool": "web_search"})
	return cb(l.turn(m))
}
func (l *flowLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *flowLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

// fakeMCP is an MCP server over streamable HTTP that remembers the
// traceparent each tool call carried.
func fakeMCP(t *testing.T) (*mcp.Client, func() string) {
	t.Helper()
	var mu sync.Mutex
	var got string
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "lookup", Version: "1"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "lookup", Description: "lookup"},
		func(_ context.Context, req *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
			mu.Lock()
			got, _ = req.Params.Meta["traceparent"].(string)
			mu.Unlock()
			return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"traceparent": "00-11111111111111111111111111111111-3333333333333333-01"},
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "found"}}}, nil, nil
		})
	h := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := mcp.NewClient(&mcp.ServerConfig{Name: "lookup", Type: mcp.ServerTypeHTTP, URL: s.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, func() string { mu.Lock(); defer mu.Unlock(); return got }
}

// One request, fanned out: a parent run, the tool call it makes, the
// sub-agent that tool starts and an MCP call into another process. Every
// line of it is in one trace, each span under the one that started it, and
// the log lines carry the same ids.
func TestAFlowIsOneTraceAcrossSubAgentsAndMCP(t *testing.T) {
	buf := &syncBuffer{}
	tw := NewTraceWriter(buf)
	var logs syncBuffer
	logger := agentgolog.New(&logs, -4, "json") // debug

	mcpClient, mcpSaw := fakeMCP(t)
	var svc *Service
	spawn := func(ctx context.Context, _ map[string]any) (any, error) {
		if _, err := mcpClient.CallTool(ctx, "lookup", map[string]any{}); err != nil {
			return nil, err
		}
		return svc.CreateSubAgent(svc.agent, "SUBTASK: answer").Run(ctx)
	}
	var err error
	svc, err = New("flow").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&flowLLM{}).
		WithObserver(tw).WithLogger(logger).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.RegisterTool(domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: "spawn", Description: "spawn",
		Parameters: map[string]any{"type": "object"}}}, spawn)

	// The flow came in over a traceparent, as a request to a host would.
	ctx := trace.WithTraceparent(context.Background(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	res, err := svc.Run(ctx, "do the thing", WithRunID("flow-run"))
	if err != nil || res == nil {
		t.Fatalf("run: %v", err)
	}

	lines := parseTrace(t, buf.String())
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	var runs []map[string]any
	spans := map[string]map[string]any{}
	for _, l := range lines {
		if l["trace_id"] != traceID {
			t.Fatalf("%v line not in the flow: %v", l["event"], l)
		}
		if l["event"] == "run_start" {
			runs = append(runs, l)
		}
		if sp, _ := l["span_id"].(string); sp != "" {
			spans[sp] = l
		}
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want parent and sub-agent", len(runs))
	}
	parent, sub := runs[0], runs[1]
	if parent["parent_span_id"] != "00f067aa0ba902b7" {
		t.Fatalf("parent run's parent = %v, want the remote caller", parent["parent_span_id"])
	}
	var spawnSpan string
	for _, l := range lines {
		if l["event"] == "tool_start" && l["tool"] == "spawn" {
			spawnSpan, _ = l["span_id"].(string)
			if l["parent_span_id"] != parent["span_id"] {
				t.Fatalf("spawn's parent = %v, want the parent run %v", l["parent_span_id"], parent["span_id"])
			}
			if args, _ := l["args"].(map[string]any); args["api_key"] != agentgolog.Redacted {
				t.Fatalf("secret argument in the trace: %v", args)
			}
		}
	}
	if sub["parent_span_id"] != spawnSpan || spawnSpan == "" {
		t.Fatalf("sub-agent's parent = %v, want the spawn tool call %v", sub["parent_span_id"], spawnSpan)
	}
	// The MCP server was sent the tool call's span, and answered with its own.
	if tp := mcpSaw(); tp != "00-"+traceID+"-"+spawnSpan+"-01" {
		t.Fatalf("MCP server saw traceparent %q, want the spawn span %s", tp, spawnSpan)
	}
	var spawnEnd map[string]any
	for _, l := range lines {
		if l["event"] == "tool_end" && l["tool"] == "spawn" {
			spawnEnd = l
		}
	}
	if remote, _ := spawnEnd["remote"].([]any); len(remote) != 1 {
		t.Fatalf("tool_end remote = %v", spawnEnd["remote"])
	}

	// The first turn's server-side work, cache write and streamed flag.
	var turn map[string]any
	for _, l := range lines {
		if l["event"] == "model_end" && l["purpose"] == "turn" && l["parent_span_id"] == parent["span_id"] {
			turn = l
			break
		}
	}
	st, _ := turn["server_tools"].(map[string]any)
	tok, _ := turn["tokens"].(map[string]any)
	if st == nil || len(st["uses"].([]any)) != 2 || st["resumes"].(float64) != 1 || tok["cache_write"].(float64) != 40 || turn["streamed"] != true {
		t.Fatalf("first turn = %v", turn)
	}
	if use := st["uses"].([]any)[0].(map[string]any); use["urls"] != nil {
		t.Fatalf("urls written outside payload mode: %v", use)
	}

	// One decision line for a choice re-made every turn of the run. Made
	// inside a model call, it is on that call's span, under the run.
	decisions := 0
	for _, l := range lines {
		if l["event"] == "decision" && l["what"] == "drop_server_tool" && l["parent_span_id"] == parent["span_id"] {
			decisions++
		}
	}
	if decisions != 1 {
		t.Fatalf("decision lines for the parent run = %d, want 1", decisions)
	}

	// The parent's run_end adds it up.
	var end map[string]any
	for _, l := range lines {
		if l["event"] == "run_end" && l["span_id"] == parent["span_id"] {
			end = l
		}
	}
	sum, _ := end["summary"].(map[string]any)
	tot, _ := sum["tokens"].(map[string]any)
	if end["outcome"] != "completed" || tot["cache_write"].(float64) != 40 || sum["tools"].(map[string]any)["spawn"] == nil {
		t.Fatalf("run_end = %v", end)
	}

	// The logs: info lines for runs, model and tool calls; debug for the
	// decision; every run line carrying the flow; no secret anywhere.
	out := logs.String()
	if strings.Contains(out, "supersecretvalue") || strings.Contains(buf.String(), "supersecretvalue") {
		t.Fatal("a secret reached the log or the trace")
	}
	var sawModel, sawTool, sawDecision bool
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(raw), &rec) != nil {
			continue
		}
		if rec["module"] != "agent.run" {
			continue
		}
		if rec["trace_id"] != traceID {
			t.Fatalf("log line without the flow: %s", raw)
		}
		switch rec["msg"] {
		case "model call":
			sawModel = true
		case "tool call":
			sawTool = true
		case "decision":
			sawDecision = true
		}
	}
	if !sawModel || !sawTool || !sawDecision {
		t.Fatalf("log lacks model/tool/decision lines (%v %v %v):\n%s", sawModel, sawTool, sawDecision, out)
	}
}

// A run resumed from a checkpoint is in the flow the checkpoint was written
// in, under the run that wrote it; and a long task picked up by a new
// process under the same task id finds the flow the same way.
func TestACheckpointCarriesItsFlow(t *testing.T) {
	buf := &syncBuffer{}
	svc, err := New("resume").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&observerToolLLM{}).
		WithObserver(NewTraceWriter(buf)).
		WithTool(NewTool("ping", "Ping back", func(_ context.Context, p *pingParams) (any, error) { return "pong", nil })).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.Run(context.Background(), "please ping", WithTaskID("task-flow")); err != nil {
		t.Fatal(err)
	}
	cp, err := svc.store.LatestTaskCheckpoint("task-flow")
	if err != nil || cp == nil {
		t.Fatalf("checkpoint: %v", err)
	}
	first := parseTrace(t, buf.String())[0]
	if first["event"] != "run_start" || cp.Traceparent != "00-"+first["trace_id"].(string)+"-"+first["span_id"].(string)+"-01" {
		t.Fatalf("checkpoint traceparent %q, run %v", cp.Traceparent, first)
	}

	// The resume path: a context in no flow joins the checkpoint's.
	buf2 := &syncBuffer{}
	svc.RegisterObserver(NewTraceWriter(buf2))
	ctx := joinCheckpointFlow(context.Background(), cp.Traceparent)
	if _, err := svc.Run(ctx, "please ping", WithTaskID("task-flow")); err != nil {
		t.Fatal(err)
	}
	resumed := parseTrace(t, buf2.String())[0]
	if resumed["trace_id"] != first["trace_id"] || resumed["parent_span_id"] != first["span_id"] {
		t.Fatalf("resumed run %v, want a child of %v in trace %v", resumed["span_id"], first["span_id"], first["trace_id"])
	}

	// A long task under the same id, started with no flow of its own.
	buf3 := &syncBuffer{}
	svc.RegisterObserver(NewTraceWriter(buf3))
	if _, err := svc.RunSegments(context.Background(), "please ping", LongRunConfig{MaxSegments: 1}, WithTaskID("task-flow")); err != nil {
		t.Fatal(err)
	}
	for _, l := range parseTrace(t, buf3.String()) {
		if l["trace_id"] != first["trace_id"] {
			t.Fatalf("long task line %v not in the checkpoint's flow %v", l["event"], first["trace_id"])
		}
	}
}

// A call made on the run's behalf says the same round, and the same span,
// at its start and its end.
func TestAnAuxCallsStartAndEndAgree(t *testing.T) {
	buf := &syncBuffer{}
	svc := buildObserverTestService(t, NewTraceWriter(buf))
	defer svc.Close()
	if _, err := svc.Run(context.Background(), "please ping"); err != nil {
		t.Fatal(err)
	}
	starts := map[string]map[string]any{}
	for _, l := range parseTrace(t, buf.String()) {
		if l["purpose"] == nil || l["purpose"] == "turn" {
			continue
		}
		sp, _ := l["span_id"].(string)
		switch l["event"] {
		case "model_start":
			if len(sp) != 16 || strings.HasPrefix(sp, "trace-") {
				t.Fatalf("aux span id %q", sp)
			}
			starts[sp] = l
		case "model_end":
			s := starts[sp]
			if s == nil {
				t.Fatalf("model_end %s without a start", sp)
			}
			if s["round"] != l["round"] {
				t.Fatalf("%v: start round %v, end round %v", l["purpose"], s["round"], l["round"])
			}
			if l["streamed"] != false || l["ttft_ms"] != nil {
				t.Fatalf("unstreamed aux call reports ttft: %v", l)
			}
		}
	}
	if len(starts) == 0 {
		t.Fatal("no aux calls traced")
	}
}

// Payload mode writes tool results whole; secrets stay out either way.
func TestTraceWriterPayloadModeAndRedaction(t *testing.T) {
	var b bytes.Buffer
	tw := NewTraceWriter(&b)
	tw.MaxTextLen = 10
	long := strings.Repeat("x", 50) + " Authorization: Bearer abcdefghijklmnopqrstuvwxyz"
	info := ToolInfo{Tool: "bash", CallID: "c", Args: map[string]any{"cmd": long, "password": "hunter22"}}
	tw.OnToolStart(context.Background(), info)
	tw.OnToolEnd(context.Background(), info, long, nil)
	tw.Payloads = true
	tw.OnToolStart(context.Background(), info)
	tw.OnToolEnd(context.Background(), info, long, nil)
	out := b.String()
	if strings.Contains(out, "abcdefghijklmnop") || strings.Contains(out, "hunter22") {
		t.Fatalf("secret in trace: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var clipped, whole map[string]any
	_ = json.Unmarshal([]byte(lines[1]), &clipped)
	_ = json.Unmarshal([]byte(lines[3]), &whole)
	if len(clipped["result"].(string)) > 20 || !strings.Contains(whole["result"].(string), strings.Repeat("x", 50)) {
		t.Fatalf("clipped=%v whole=%v", clipped["result"], whole["result"])
	}
}
