package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

func oneRunConstraints(t *testing.T, rec *traceDecisionSink) trace.Decision {
	t.Helper()
	got := rec.named("run_constraints")
	if len(got) != 1 {
		t.Fatalf("run_constraints decisions = %d, want exactly 1", len(got))
	}
	if got[0].Reason == "" {
		t.Error("run_constraints has no reason")
	}
	return got[0]
}

func TestRunConstraintsTraceExtracted(t *testing.T) {
	llm := &constraintLLM{forbidTools: true, deliverables: `[{"kind":"file","description":"write hello.txt with hi","path":"hello.txt"}]`}
	svc := newConstraintTestService(t, llm)
	rec := &traceDecisionSink{}
	got := svc.resolveRunConstraints(trace.WithSink(context.Background(), rec), "SECRET-GOAL-TEXT", DefaultRunConfig())
	if got.Empty() {
		t.Fatal("extraction returned nothing")
	}
	d := oneRunConstraints(t, rec)
	if d.Detail["source"] != constraintSourceExtracted || d.Detail["forbid_tools"] != true {
		t.Errorf("detail = %v", d.Detail)
	}
	dl, _ := d.Detail["deliverables"].([]map[string]any)
	if len(dl) != 1 || dl[0]["kind"] != "file" || dl[0]["path"] != "hello.txt" {
		t.Errorf("deliverables = %v", d.Detail["deliverables"])
	}
	if strings.Contains(d.Reason, "SECRET-GOAL-TEXT") {
		t.Error("goal text leaked into the decision")
	}
}

func TestRunConstraintsTraceExtractedEmpty(t *testing.T) {
	svc := newConstraintTestService(t, &constraintLLM{})
	rec := &traceDecisionSink{}
	svc.resolveRunConstraints(trace.WithSink(context.Background(), rec), "what is 2+2", DefaultRunConfig())
	d := oneRunConstraints(t, rec)
	if d.Detail["source"] != constraintSourceExtracted || d.Detail["forbid_tools"] != false {
		t.Errorf("detail = %v", d.Detail)
	}
}

func TestRunConstraintsTraceCallerSupplied(t *testing.T) {
	llm := &constraintLLM{}
	svc := newConstraintTestService(t, llm)
	rec := &traceDecisionSink{}
	cfg := DefaultRunConfig()
	cfg.ToolsDisabled = true
	svc.resolveRunConstraints(trace.WithSink(context.Background(), rec), "x", cfg)
	d := oneRunConstraints(t, rec)
	if d.Detail["source"] != constraintSourceCaller || !strings.Contains(d.Reason, "WithToolsDisabled") {
		t.Errorf("decision = %+v", d)
	}
	if llm.extractCalls != 0 {
		t.Error("declared constraints still extracted")
	}
}

func TestRunConstraintsTraceDisabled(t *testing.T) {
	svc := newConstraintTestService(t, &constraintLLM{})
	rec := &traceDecisionSink{}
	cfg := DefaultRunConfig()
	cfg.DisableConstraintExtraction = true
	svc.resolveRunConstraints(trace.WithSink(context.Background(), rec), "x", cfg)
	if d := oneRunConstraints(t, rec); d.Detail["source"] != constraintSourceDisabled {
		t.Errorf("detail = %v", d.Detail)
	}
}

func TestRunConstraintsTraceExtractionFailure(t *testing.T) {
	svc := newConstraintTestService(t, &constraintLLM{})
	svc.llmService = &failingStructuredLLM{inner: &constraintLLM{}}
	rec := &traceDecisionSink{}
	svc.resolveRunConstraints(trace.WithSink(context.Background(), rec), "send an email", DefaultRunConfig())
	d := oneRunConstraints(t, rec)
	if d.Detail["source"] != constraintSourceFailed || !strings.Contains(d.Reason, "extraction failed") {
		t.Errorf("decision = %+v", d)
	}
}

type constraintDecisionObserver struct {
	BaseObserver
	mu   sync.Mutex
	seen []RuntimeDecisionInfo
}

func (o *constraintDecisionObserver) OnRuntimeDecision(_ context.Context, info RuntimeDecisionInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if info.What == "run_constraints" {
		o.seen = append(o.seen, info)
	}
}

// A real run reaches observers (and so TraceWriter and ActivityLog) with
// exactly one run_constraints decision, however many rounds it takes.
func TestRunConstraintsTraceReachesObserversOncePerRun(t *testing.T) {
	obs := &constraintDecisionObserver{}
	svc, err := New("constraints").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&constraintLLM{replies: []string{"working", "done"}}).
		WithObserver(obs).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if _, err := svc.Run(context.Background(), "Do something involved.", WithMaxTurns(3)); err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.seen) != 1 {
		t.Fatalf("observer heard %d run_constraints decisions, want 1", len(obs.seen))
	}
	if obs.seen[0].Detail["source"] != constraintSourceExtracted {
		t.Errorf("detail = %v", obs.seen[0].Detail)
	}
}
