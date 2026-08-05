package otelobserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func newRecorder() (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	return sr, tp
}

func attrMap(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := make(map[string]attribute.Value)
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func findSpan(spans []sdktrace.ReadOnlySpan, kind string) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if attrMap(s)[attrSpanKind].AsString() == kind {
			return s
		}
	}
	return nil
}

// TestObserverDirect drives the Observer callbacks directly with fabricated
// payloads sharing a TaskID and asserts the span tree + OpenInference attrs.
func TestObserverDirect(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)

	const taskID = "task-123"
	const sessionID = "sess-abc"
	ctx := context.Background()

	// Model turn.
	mi := agent.ModelInfo{TaskID: taskID, SessionID: sessionID, AgentName: "weather-bot", Round: 1, SpanID: "span-1"}
	obs.OnModelStart(ctx, mi)
	obs.OnModelDelta(ctx, agent.ModelDelta{SpanID: "span-1", Kind: "partial", Text: "hi"})
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{Content: "answer", TokensUsed: 42, ToolCalls: 1}, nil)

	// Tool call.
	ti := agent.ToolInfo{TaskID: taskID, SessionID: sessionID, AgentName: "weather-bot", Tool: "get_weather", CallID: "call-1", Args: map[string]any{"city": "Tokyo"}}
	obs.OnToolStart(ctx, ti)
	obs.OnToolEnd(ctx, ti, map[string]any{"temp_c": 22}, nil)

	// Terminal checkpoint -> ends root.
	obs.OnCheckpoint(ctx, agent.CheckpointInfo{TaskID: taskID, SessionID: sessionID, Reason: "task_completed", Round: 2, Messages: 4, FinalText: "answer"})

	spans := sr.Ended()
	if len(spans) != 3 {
		t.Fatalf("expected 3 ended spans (llm, tool, root), got %d", len(spans))
	}

	root := findSpan(spans, spanKindChain)
	llm := findSpan(spans, spanKindLLM)
	tool := findSpan(spans, spanKindTool)
	if root == nil || llm == nil || tool == nil {
		t.Fatalf("missing span kinds: root=%v llm=%v tool=%v", root != nil, llm != nil, tool != nil)
	}

	// Same trace + parent-child nesting.
	if root.SpanContext().TraceID() != llm.SpanContext().TraceID() {
		t.Error("llm span not in same trace as root")
	}
	if llm.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Error("llm span is not a child of root")
	}
	if tool.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Error("tool span is not a child of root")
	}

	// OpenInference attributes.
	lm := attrMap(llm)
	if lm[attrLLMTokenTotal].AsInt64() != 42 {
		t.Errorf("llm.token_count.total = %d, want 42", lm[attrLLMTokenTotal].AsInt64())
	}
	if lm[attrOutputValue].AsString() != "answer" {
		t.Errorf("llm output.value = %q", lm[attrOutputValue].AsString())
	}
	tm := attrMap(tool)
	if tm[attrToolName].AsString() != "get_weather" {
		t.Errorf("tool.name = %q", tm[attrToolName].AsString())
	}
	if tm[attrInputValue].AsString() == "" {
		t.Error("tool input.value empty")
	}
	if tm[attrOutputValue].AsString() == "" {
		t.Error("tool output.value empty")
	}
	if attrMap(root)[attrSessionID].AsString() != sessionID {
		t.Errorf("root session.id = %q", attrMap(root)[attrSessionID].AsString())
	}
}

func TestObserverErrorStatus(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)
	ctx := context.Background()

	ti := agent.ToolInfo{TaskID: "t1", Tool: "boom", CallID: "c1"}
	obs.OnToolStart(ctx, ti)
	obs.OnToolEnd(ctx, ti, nil, errors.New("kaboom"))
	obs.OnCheckpoint(ctx, agent.CheckpointInfo{TaskID: "t1", Reason: "task_blocked"})

	tool := findSpan(sr.Ended(), spanKindTool)
	if tool == nil {
		t.Fatal("no tool span")
	}
	if tool.Status().Code != codes.Error {
		t.Errorf("tool status = %v, want Error", tool.Status().Code)
	}
	if len(tool.Events()) == 0 {
		t.Error("expected recorded error event on tool span")
	}
}

func TestShutdownEndsDanglingRoots(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)
	ctx := context.Background()

	// A task that starts a model turn but never checkpoints.
	mi := agent.ModelInfo{TaskID: "dangling", SpanID: "s1"}
	obs.OnModelStart(ctx, mi)
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{}, nil)

	if len(sr.Ended()) != 1 {
		t.Fatalf("only llm span should be ended before shutdown, got %d", len(sr.Ended()))
	}
	if err := obs.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if findSpan(sr.Ended(), spanKindChain) == nil {
		t.Error("dangling root span was not ended by Shutdown")
	}
}

// ---- end-to-end via a real Service + mock LLM ----------------------------

type scriptedLLM struct{ calls int32 }

func (l *scriptedLLM) turn() *domain.GenerationResult {
	if atomic.AddInt32(&l.calls, 1) == 1 {
		return &domain.GenerationResult{
			ToolCalls: []domain.ToolCall{{
				ID:       "call_weather_1",
				Function: domain.FunctionCall{Name: "get_weather", Arguments: map[string]interface{}{"city": "Tokyo"}},
			}},
		}
	}
	return &domain.GenerationResult{Content: "22C sunny"}
}

func (l *scriptedLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *scriptedLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *scriptedLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.turn(), nil
}
func (l *scriptedLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.turn())
}
func (l *scriptedLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: `{}`}, nil
}
func (l *scriptedLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return &domain.IntentResult{Intent: domain.IntentAction, Confidence: 0.9}, nil
}

type weatherParams struct {
	City string `json:"city" desc:"City"`
}

func TestObserverViaService(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)

	home := t.TempDir()
	cfg := &config.Config{
		Home: home,
		RAG:  config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{
			StoreType:  "file",
			MemoryPath: filepath.Join(home, "data", "memories"),
		},
	}
	cfg.ApplyHomeLayout()

	svc, err := agent.New("weather-bot").
		WithPTC(false).
		WithConfig(cfg).
		WithLLM(&scriptedLLM{}).
		WithObserver(obs).
		WithTool(agent.NewTool("get_weather", "Get weather",
			func(_ context.Context, p *weatherParams) (any, error) {
				return map[string]any{"city": p.City, "temp_c": 22}, nil
			})).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "weather in Tokyo?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = agent.Concat(events)
	_ = obs.Shutdown(context.Background())

	spans := sr.Ended()
	if findSpan(spans, spanKindChain) == nil {
		t.Error("no root/CHAIN span from service run")
	}
	if findSpan(spans, spanKindLLM) == nil {
		t.Error("no LLM span from service run")
	}
	if findSpan(spans, spanKindTool) == nil {
		t.Error("no TOOL span from service run")
	}
}

// Ensure the OTLP/Phoenix wiring constructs without a live collector.
func TestPhoenixWiring(t *testing.T) {
	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "")
	os.Unsetenv("PHOENIX_COLLECTOR_ENDPOINT")
	obs, shutdown, err := Phoenix(context.Background(), WithEndpoint("http://localhost:6006/v1/traces"))
	if err != nil {
		t.Fatalf("Phoenix: %v", err)
	}
	if obs == nil || shutdown == nil {
		t.Fatal("nil observer or shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
