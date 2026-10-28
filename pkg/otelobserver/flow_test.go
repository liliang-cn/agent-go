package otelobserver

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agtrace "github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// The spans in an OTel backend and the lines in a trace file are the same
// spans under the same ids: the root is the run, the llm and tool spans are
// the model and tool calls, and the trace is the flow — including the remote
// parent a traceparent named.
func TestSpansUseTheFlowIDs(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithIDGenerator(IDGenerator()))
	obs := New(tp)

	ctx := agtrace.WithTraceparent(context.Background(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx, run := agtrace.StartSpan(ctx)
	modelSpan := agtrace.NewSpanID()
	mi := agent.ModelInfo{TaskID: "t", SessionID: "s", Round: 1, SpanID: modelSpan, Purpose: "turn"}
	obs.OnModelStart(ctx, mi)
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{Content: "a", PromptTokens: 10, CachedTokens: 4, CacheWriteTokens: 6, Streamed: true, TTFTMs: 5,
		ServerTools: &domain.ServerToolReport{Uses: []domain.ServerToolUse{{Name: "web_search", Input: map[string]any{"query": "q"}, Results: 3}}, Resumes: 1, RequestMs: []int64{10, 20}, Citations: 2}}, nil)
	ti := agent.ToolInfo{TaskID: "t", SessionID: "s", Tool: "bash", CallID: "c1", SpanID: agtrace.ChildSpanID(run, "c1")}
	obs.OnToolStart(ctx, ti)
	obs.OnToolEnd(ctx, ti, "ok", nil)
	obs.OnCheckpoint(ctx, agent.CheckpointInfo{TaskID: "t", SessionID: "s", Reason: "task_complete"})

	spans := sr.Ended()
	llm := findSpan(spans, spanKindLLM)
	tool := findSpan(spans, spanKindTool)
	root := findSpan(spans, spanKindChain)
	if llm == nil || tool == nil || root == nil {
		t.Fatalf("spans: %d", len(spans))
	}
	for _, s := range []sdktrace.ReadOnlySpan{llm, tool, root} {
		if s.SpanContext().TraceID().String() != run.TraceID {
			t.Fatalf("%s trace id %s, want the flow's %s", s.Name(), s.SpanContext().TraceID(), run.TraceID)
		}
	}
	if root.SpanContext().SpanID().String() != run.SpanID || root.Parent().SpanID().String() != run.ParentSpanID {
		t.Fatalf("root span %s parent %s, want the run %s under %s", root.SpanContext().SpanID(), root.Parent().SpanID(), run.SpanID, run.ParentSpanID)
	}
	if llm.SpanContext().SpanID().String() != modelSpan || tool.SpanContext().SpanID().String() != ti.SpanID {
		t.Fatalf("llm %s tool %s, want %s %s", llm.SpanContext().SpanID(), tool.SpanContext().SpanID(), modelSpan, ti.SpanID)
	}
	a := attrMap(llm)
	if a[attrLLMTokenCacheWrite].AsInt64() != 6 || a["agentgo.server_tools.uses"].AsInt64() != 1 || a["agentgo.server_tools.resumes"].AsInt64() != 1 ||
		a["agentgo.server_tools.queries"].AsStringSlice()[0] != "q" || !a["agentgo.llm.streamed"].AsBool() || a[genAIUsageCacheWrite].AsInt64() != 6 {
		t.Fatalf("llm attrs: %v", a)
	}
	if len(llm.Events()) == 0 || llm.Events()[len(llm.Events())-1].Name != "server_tool.use" {
		t.Fatalf("llm events: %v", llm.Events())
	}
}
