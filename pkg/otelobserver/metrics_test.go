package otelobserver

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func newMeter() (*sdkmetric.ManualReader, *sdkmetric.MeterProvider) {
	r := sdkmetric.NewManualReader()
	return r, sdkmetric.NewMeterProvider(sdkmetric.WithReader(r))
}

func collect(t *testing.T, r *sdkmetric.ManualReader) *metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return &rm
}

// sumOf totals every data point of a counter, or counts every observation of a
// histogram. Missing reports false so a test can tell "zero" from "never
// recorded".
func sumOf(rm *metricdata.ResourceMetrics, name string) (float64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				var total int64
				for _, dp := range d.DataPoints {
					total += dp.Value
				}
				return float64(total), true
			case metricdata.Sum[float64]:
				var total float64
				for _, dp := range d.DataPoints {
					total += dp.Value
				}
				return total, true
			case metricdata.Histogram[float64]:
				var count uint64
				for _, dp := range d.DataPoints {
					count += dp.Count
				}
				return float64(count), true
			}
		}
	}
	return 0, false
}

func mustSum(t *testing.T, rm *metricdata.ResourceMetrics, name string) float64 {
	t.Helper()
	v, ok := sumOf(rm, name)
	if !ok {
		t.Fatalf("metric %q was never recorded", name)
	}
	return v
}

// attrsOf returns the attribute sets of a metric's data points.
func attrsOf(rm *metricdata.ResourceMetrics, name string) []attribute.Set {
	var out []attribute.Set
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					out = append(out, dp.Attributes)
				}
			case metricdata.Sum[float64]:
				for _, dp := range d.DataPoints {
					out = append(out, dp.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					out = append(out, dp.Attributes)
				}
			}
		}
	}
	return out
}

func hasAttr(sets []attribute.Set, key, want string) bool {
	for _, s := range sets {
		if v, ok := s.Value(attribute.Key(key)); ok && v.AsString() == want {
			return true
		}
	}
	return false
}

// TestNoMeterProviderIsNoOp pins the promise that New(tp) is unchanged: with
// no MeterProvider the metric side does not exist, and every callback still
// has to be safe to call.
func TestNoMeterProviderIsNoOp(t *testing.T) {
	_, tp := newRecorder()
	obs := New(tp)
	if obs.metrics != nil {
		t.Fatal("instruments built without a MeterProvider")
	}
	ctx := context.Background()
	mi := agent.ModelInfo{TaskID: "t", SpanID: "s", Model: "gpt-4o"}
	obs.OnModelStart(ctx, mi)
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{PromptTokens: 10}, nil)
	obs.OnLint(ctx, agent.LintInfo{TaskID: "t", Lint: "x"})
	obs.OnError(ctx, agent.ErrorInfo{TaskID: "t"})
	obs.OnCompaction(ctx, agent.CompactionInfo{TaskID: "t"})
	obs.OnModelRetry(ctx, agent.ModelRetryInfo{TaskID: "t", SpanID: "s"})
	_ = obs.Shutdown(ctx)
}

// TestMetricsFromCallbacks drives every instrumented callback once with known
// numbers and asserts what the reader collects — including the cost, which is
// the one metric derived rather than reported.
func TestMetricsFromCallbacks(t *testing.T) {
	_, tp := newRecorder()
	reader, mp := newMeter()
	obs := New(tp, WithMeterProvider(mp))
	ctx := context.Background()

	const taskID = "task-m"
	mi := agent.ModelInfo{TaskID: taskID, AgentName: "bot", Model: "gpt-4o", SpanID: "s1", Round: 1}
	obs.OnModelStart(ctx, mi)
	obs.OnModelRetry(ctx, agent.ModelRetryInfo{
		TaskID: taskID, AgentName: "bot", SpanID: "s1", Kind: "max_tokens_truncation",
		Attempt: 1, Reason: "length", MaxTokensFrom: 2000, MaxTokensTo: 8000,
		Delay: 250 * time.Millisecond,
	})
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{
		Content: "hi", DurationMs: 1500, TokensUsed: 1200,
		PromptTokens: 1000, CachedTokens: 400, CompletionTokens: 200,
	}, nil)

	ti := agent.ToolInfo{TaskID: taskID, AgentName: "bot", Tool: "echo", CallID: "c1"}
	obs.OnToolStart(ctx, ti)
	obs.OnToolEnd(ctx, ti, map[string]any{"ok": true}, nil)

	tf := agent.ToolInfo{TaskID: taskID, AgentName: "bot", Tool: "boom", CallID: "c2"}
	obs.OnToolStart(ctx, tf)
	obs.OnToolEnd(ctx, tf, nil, errors.New("kaboom"))

	obs.OnLint(ctx, agent.LintInfo{TaskID: taskID, AgentName: "bot", Lint: "non_empty_final_answer", Reason: "empty", Retrying: true})
	obs.OnCompaction(ctx, agent.CompactionInfo{TaskID: taskID, AgentName: "bot", Trigger: "token_threshold", MessagesBefore: 40, MessagesAfter: 12})
	obs.OnError(ctx, agent.ErrorInfo{TaskID: taskID, AgentName: "bot", Marker: "history_persist_failed", Message: "database is closed"})
	obs.OnCheckpoint(ctx, agent.CheckpointInfo{TaskID: taskID, Reason: "task_completed"})

	rm := collect(t, reader)

	if got := mustSum(t, rm, "agentgo.model.calls"); got != 1 {
		t.Errorf("model.calls = %v, want 1", got)
	}
	if got := mustSum(t, rm, "agentgo.model.retries"); got != 1 {
		t.Errorf("model.retries = %v, want 1", got)
	}
	if got := mustSum(t, rm, "agentgo.model.duration"); got != 1 {
		t.Errorf("model.duration observations = %v, want 1", got)
	}
	if got := mustSum(t, rm, "agentgo.tool.calls"); got != 2 {
		t.Errorf("tool.calls = %v, want 2", got)
	}
	if got := mustSum(t, rm, "agentgo.tool.duration"); got != 2 {
		t.Errorf("tool.duration observations = %v, want 2", got)
	}
	if got := mustSum(t, rm, "agentgo.lint.rejections"); got != 1 {
		t.Errorf("lint.rejections = %v, want 1", got)
	}
	if got := mustSum(t, rm, "agentgo.compactions"); got != 1 {
		t.Errorf("compactions = %v, want 1", got)
	}
	if got := mustSum(t, rm, "agentgo.errors"); got != 1 {
		t.Errorf("errors = %v, want 1", got)
	}
	if got := mustSum(t, rm, "agentgo.tokens.prompt"); got != 1000 {
		t.Errorf("tokens.prompt = %v, want 1000", got)
	}
	if got := mustSum(t, rm, "agentgo.tokens.completion"); got != 200 {
		t.Errorf("tokens.completion = %v, want 200", got)
	}
	if got := mustSum(t, rm, "agentgo.tokens.cached"); got != 400 {
		t.Errorf("tokens.cached = %v, want 400", got)
	}

	// gpt-4o: 0.005/1K in (no cache discount), 0.015/1K out.
	// 1000 prompt + 200 completion => 0.005 + 0.003.
	want := 0.008
	if got := mustSum(t, rm, "agentgo.cost.usd"); math.Abs(got-want) > 1e-9 {
		t.Errorf("cost.usd = %v, want %v", got, want)
	}
	if _, ok := sumOf(rm, "agentgo.model.unpriced_turns"); ok {
		t.Error("a priced model was counted as unpriced")
	}

	// Attributes: agent and model where known, and never a task or session id.
	callAttrs := attrsOf(rm, "agentgo.model.calls")
	if !hasAttr(callAttrs, mAttrAgent, "bot") || !hasAttr(callAttrs, mAttrModel, "gpt-4o") {
		t.Errorf("model.calls attributes = %v", callAttrs)
	}
	if !hasAttr(callAttrs, mAttrStatus, statusOK) {
		t.Error("model.calls missing an ok status")
	}
	for _, name := range []string{
		"agentgo.model.calls", "agentgo.tool.calls", "agentgo.cost.usd",
		"agentgo.lint.rejections", "agentgo.errors", "agentgo.compactions",
	} {
		for _, set := range attrsOf(rm, name) {
			for _, kv := range set.ToSlice() {
				switch string(kv.Key) {
				case attrTaskID, attrSessionID, "task_id":
					t.Errorf("%s carries a per-run id attribute %q", name, kv.Key)
				}
			}
		}
	}

	toolAttrs := attrsOf(rm, "agentgo.tool.calls")
	if !hasAttr(toolAttrs, mAttrTool, "echo") || !hasAttr(toolAttrs, mAttrTool, "boom") {
		t.Errorf("tool.calls attributes = %v", toolAttrs)
	}
	if !hasAttr(toolAttrs, mAttrStatus, statusError) {
		t.Error("the failing tool call was not marked as an error")
	}
	if !hasAttr(attrsOf(rm, "agentgo.lint.rejections"), mAttrVerdict, "retrying") {
		t.Error("lint verdict attribute missing")
	}
	if !hasAttr(attrsOf(rm, "agentgo.errors"), mAttrKind, "history_persist_failed") {
		t.Error("error kind attribute missing")
	}
}

// TestUnpricedModelIsCountedNotPriced is the distinction CalculateCostDetailed
// exists for: nothing knows this model's rates, so the cost counter must stay
// silent rather than add a zero that reads as free.
func TestUnpricedModelIsCountedNotPriced(t *testing.T) {
	_, tp := newRecorder()
	reader, mp := newMeter()
	obs := New(tp, WithMeterProvider(mp))
	ctx := context.Background()

	mi := agent.ModelInfo{TaskID: "t", AgentName: "bot", Model: "some-unlisted-model-9000", SpanID: "s1"}
	obs.OnModelStart(ctx, mi)
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{PromptTokens: 500, CompletionTokens: 100}, nil)

	rm := collect(t, reader)
	if got := mustSum(t, rm, "agentgo.model.unpriced_turns"); got != 1 {
		t.Errorf("unpriced_turns = %v, want 1", got)
	}
	if v, ok := sumOf(rm, "agentgo.cost.usd"); ok && v != 0 {
		t.Errorf("cost.usd = %v for an unpriced model, want no contribution", v)
	}
	if got := mustSum(t, rm, "agentgo.tokens.prompt"); got != 500 {
		t.Errorf("tokens.prompt = %v, want 500", got)
	}
}

// TestSpanEventsForDeliberation asserts the five callbacks that were missing
// land as events on the span an operator is looking at.
func TestSpanEventsForDeliberation(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)
	ctx := context.Background()
	const taskID = "task-e"

	mi := agent.ModelInfo{TaskID: taskID, AgentName: "bot", SpanID: "s1", Round: 3}
	obs.OnModelStart(ctx, mi)
	obs.OnModelRetry(ctx, agent.ModelRetryInfo{
		TaskID: taskID, SpanID: "s1", Kind: "transient_error", Attempt: 2,
		Reason: "502 bad gateway", Delay: time.Second,
	})
	obs.OnModelEnd(ctx, mi, &agent.ModelResult{Content: "draft"}, nil)

	obs.OnLint(ctx, agent.LintInfo{TaskID: taskID, Lint: "file_task_must_write", Reason: "no file", Retrying: false})
	obs.OnCompaction(ctx, agent.CompactionInfo{TaskID: taskID, Trigger: "token_threshold", MessagesBefore: 30, MessagesAfter: 9, ContextTokens: 25000})
	obs.OnError(ctx, agent.ErrorInfo{TaskID: taskID, Marker: "", Message: "tool failed"})
	obs.OnCheckpoint(ctx, agent.CheckpointInfo{TaskID: taskID, Reason: "task_blocked"})

	spans := sr.Ended()
	llm := findSpan(spans, spanKindLLM)
	if llm == nil {
		t.Fatal("no llm span")
	}
	if !hasEvent(llm, "model.retry") {
		t.Error("the retry was not recorded on the turn it happened inside")
	}
	retry := eventAttrs(llm, "model.retry")
	if retry["retry.kind"].AsString() != "transient_error" {
		t.Errorf("retry.kind = %q", retry["retry.kind"].AsString())
	}
	if retry["retry.delay_ms"].AsInt64() != 1000 {
		t.Errorf("retry.delay_ms = %d", retry["retry.delay_ms"].AsInt64())
	}

	root := findSpan(spans, spanKindChain)
	if root == nil {
		t.Fatal("no root span")
	}
	lint := eventAttrs(root, "lint.rejected")
	if lint["lint.name"].AsString() != "file_task_must_write" {
		t.Errorf("lint.name = %q", lint["lint.name"].AsString())
	}
	if lint["lint.verdict"].AsString() != "blocked" {
		t.Errorf("lint.verdict = %q, want blocked", lint["lint.verdict"].AsString())
	}
	comp := eventAttrs(root, "context.compaction")
	if comp["compaction.messages.before"].AsInt64() != 30 || comp["compaction.messages.after"].AsInt64() != 9 {
		t.Errorf("compaction before/after = %v/%v",
			comp["compaction.messages.before"].AsInt64(), comp["compaction.messages.after"].AsInt64())
	}
	if comp["compaction.tokens.context"].AsInt64() != 25000 {
		t.Errorf("compaction.tokens.context = %d", comp["compaction.tokens.context"].AsInt64())
	}
	errEv := eventAttrs(root, "agentgo.error")
	if errEv["error.kind"].AsString() != "unmarked" {
		t.Errorf("error.kind = %q, want unmarked", errEv["error.kind"].AsString())
	}
}

// TestSegmentSpansNestUnderTheirRoot pins the ordering the refcount exists
// for: a segment's run checkpoints before the segment ends, so the root must
// outlive the checkpoint and close only once its segment child has.
func TestSegmentSpansNestUnderTheirRoot(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)
	ctx := context.Background()
	const taskID = "task-seg"

	for i := range 2 {
		obs.OnSegment(ctx, agent.SegmentInfo{TaskID: taskID, Index: i, Total: 2, SessionID: "sess-" + string(rune('a'+i))})
		mi := agent.ModelInfo{TaskID: taskID, SpanID: "s" + string(rune('0'+i)), Round: 1}
		obs.OnModelStart(ctx, mi)
		obs.OnModelEnd(ctx, mi, &agent.ModelResult{Content: "step"}, nil)
		obs.OnCheckpoint(ctx, agent.CheckpointInfo{TaskID: taskID, Reason: "task_completed"})
		obs.OnSegment(ctx, agent.SegmentInfo{
			TaskID: taskID, Index: i, Total: 2, Ending: true,
			StopReason: agent.StopReasonMaxTurns, Duration: time.Second, Productive: true,
		})
	}

	spans := sr.Ended()
	var segments, roots []sdktrace.ReadOnlySpan
	for _, s := range spans {
		switch s.Name() {
		case "segment 0", "segment 1":
			segments = append(segments, s)
		default:
			if attrMap(s)[attrSpanKind].AsString() == spanKindChain {
				roots = append(roots, s)
			}
		}
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segment spans, got %d", len(segments))
	}
	if len(roots) != 2 {
		t.Fatalf("expected one root per segment, got %d", len(roots))
	}
	rootIDs := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, r := range roots {
		rootIDs[r.SpanContext().SpanID()] = r
	}
	for _, seg := range segments {
		root, ok := rootIDs[seg.Parent().SpanID()]
		if !ok {
			t.Fatalf("%s is not a child of any task root", seg.Name())
		}
		// The checkpoint fires before the segment ends; the root must not
		// have closed ahead of its own child.
		if root.EndTime().Before(seg.EndTime()) {
			t.Errorf("%s outlived the root it hangs from", seg.Name())
		}
	}
	// The model turns nest under their segment, not flat under the root.
	segIDs := map[trace.SpanID]bool{}
	for _, s := range segments {
		segIDs[s.SpanContext().SpanID()] = true
	}
	for _, s := range spans {
		if attrMap(s)[attrSpanKind].AsString() != spanKindLLM {
			continue
		}
		if !segIDs[s.Parent().SpanID()] {
			t.Error("a model turn attached to the root while a segment was open")
		}
	}
	if attrMap(segments[0])["agentgo.segment.productive"].AsBool() != true {
		t.Error("segment.productive not recorded")
	}
	if attrMap(segments[0])[attrTaskID].AsString() != taskID {
		t.Error("segment span does not carry the task id that correlates it")
	}
}

// TestMetricsFromScriptedRun drives the real loop over a scripted model and
// asserts the bridge counts what actually happened. It is the difference
// between "the callbacks work" and "the runtime calls them".
func TestMetricsFromScriptedRun(t *testing.T) {
	sr, tp := newRecorder()
	reader, mp := newMeter()
	obs := New(tp, WithMeterProvider(mp))

	llm := extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
		extensiontest.Answer("done"),
	)
	svc, err := extensiontest.NewServiceWithBuilder(
		agent.New("otel-bot").WithLLM(llm).WithObserver(obs).WithExtensions(extensiontest.EchoTool()),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	out := extensiontest.Run(t, svc, "say hi")
	if out.Final != "done" {
		t.Fatalf("final = %q, want done", out.Final)
	}
	_ = obs.Shutdown(context.Background())

	rm := collect(t, reader)
	if got := mustSum(t, rm, "agentgo.model.calls"); got < 2 {
		t.Errorf("model.calls = %v, want at least 2", got)
	}
	if got := mustSum(t, rm, "agentgo.tool.calls"); got < 1 {
		t.Errorf("tool.calls = %v, want at least 1", got)
	}
	if !hasAttr(attrsOf(rm, "agentgo.tool.calls"), mAttrTool, "echo") {
		t.Error("the echo call was not attributed to its tool")
	}
	if !hasAttr(attrsOf(rm, "agentgo.model.calls"), mAttrAgent, "otel-bot") {
		t.Error("model.calls not attributed to the agent")
	}

	spans := sr.Ended()
	if findSpan(spans, spanKindChain) == nil {
		t.Error("no root span from the scripted run")
	}
	if findSpan(spans, spanKindLLM) == nil {
		t.Error("no llm span from the scripted run")
	}
	if findSpan(spans, spanKindTool) == nil {
		t.Error("no tool span from the scripted run")
	}
}

func hasEvent(s sdktrace.ReadOnlySpan, name string) bool {
	for _, e := range s.Events() {
		if e.Name == name {
			return true
		}
	}
	return false
}

func eventAttrs(s sdktrace.ReadOnlySpan, name string) map[string]attribute.Value {
	out := make(map[string]attribute.Value)
	for _, e := range s.Events() {
		if e.Name != name {
			continue
		}
		for _, kv := range e.Attributes {
			out[string(kv.Key)] = kv.Value
		}
	}
	return out
}
