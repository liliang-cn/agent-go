package otelobserver

import (
	"context"
	"errors"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// The tests in this file are the ones a live probe found and the unit suite
// did not. Every one of them asserts an invariant BETWEEN two accounts of the
// same run — the bridge's and the runtime's — rather than that the bridge
// echoes what it was handed, which is all a single-sided test can check.

// TestRoundEndCheckpointDoesNotEndTheRoot pins the split-trace bug.
//
// AutonomyProfile.CheckpointEveryRounds fires OnCheckpoint mid-run with reason
// "round_end". The bridge treated any checkpoint as the run's end, so a run
// with per-round snapshots produced one root span per round: the live probe
// showed two "task <id>" roots for a two-round run, the first holding the tool
// calls and the second holding the answer.
func TestRoundEndCheckpointDoesNotEndTheRoot(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)

	const taskID = "task-split"
	turn := func(spanID string, round int) {
		info := agent.ModelInfo{TaskID: taskID, SessionID: "sess", AgentName: "bot", Round: round, SpanID: spanID, Model: "m"}
		obs.OnModelStart(context.Background(), info)
		obs.OnModelEnd(context.Background(), info, &agent.ModelResult{Content: "x", DurationMs: 5}, nil)
	}

	turn("span-1", 1)
	obs.OnCheckpoint(context.Background(), agent.CheckpointInfo{
		TaskID: taskID, SessionID: "sess", Reason: string(agent.CheckpointReasonRoundEnd), Round: 1, Messages: 4,
	})
	turn("span-2", 2)
	obs.OnCheckpoint(context.Background(), agent.CheckpointInfo{
		TaskID: taskID, SessionID: "sess", Reason: string(agent.CheckpointReasonTaskComplete), Round: 2, Messages: 8,
		FinalText: "done",
	})
	_ = obs.Shutdown(context.Background())

	spans := sr.Ended()
	var roots []sdktrace.ReadOnlySpan
	var llms []sdktrace.ReadOnlySpan
	for _, s := range spans {
		switch attrMap(s)[attrSpanKind].AsString() {
		case spanKindChain:
			roots = append(roots, s)
		case spanKindLLM:
			llms = append(llms, s)
		}
	}
	if len(roots) != 1 {
		t.Fatalf("got %d root spans for one run, want 1 (a mid-run checkpoint split the trace)", len(roots))
	}
	root := roots[0]
	if len(llms) != 2 {
		t.Fatalf("got %d llm spans, want 2", len(llms))
	}
	for _, s := range llms {
		if s.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Errorf("llm span %q did not nest under the single root", s.Name())
		}
	}
	// Both checkpoints still show on the timeline; only the terminal one ends
	// the span.
	var checkpoints int
	for _, e := range root.Events() {
		if e.Name == "checkpoint" {
			checkpoints++
		}
	}
	if checkpoints != 2 {
		t.Errorf("root carries %d checkpoint events, want 2", checkpoints)
	}
	if got := attrMap(root)["agentgo.checkpoint.reason"].AsString(); got != string(agent.CheckpointReasonTaskComplete) {
		t.Errorf("root checkpoint reason = %q, want the terminal one", got)
	}
	if got := attrMap(root)[attrOutputValue].AsString(); got != "done" {
		t.Errorf("root output.value = %q, want the terminal answer", got)
	}
}

// TestAfterToolCheckpointIsAlsoNonTerminal covers the other snapshot reason
// that fires mid-run.
func TestAfterToolCheckpointIsAlsoNonTerminal(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)

	info := agent.ModelInfo{TaskID: "t", SessionID: "s", SpanID: "sp", Model: "m"}
	obs.OnModelStart(context.Background(), info)
	obs.OnModelEnd(context.Background(), info, &agent.ModelResult{}, nil)
	obs.OnCheckpoint(context.Background(), agent.CheckpointInfo{
		TaskID: "t", Reason: string(agent.CheckpointReasonAfterTool), Round: 1,
	})
	if n := len(sr.Ended()); n != 1 {
		t.Fatalf("%d spans ended after an after_tool checkpoint, want only the llm span", n)
	}
	obs.OnCheckpoint(context.Background(), agent.CheckpointInfo{
		TaskID: "t", Reason: string(agent.CheckpointReasonTaskBlocked), Round: 1,
	})
	if n := len(sr.Ended()); n != 2 {
		t.Fatalf("%d spans ended after the terminal checkpoint, want 2", n)
	}
}

// TestModelDurationCountsTurnsThatReturnedNothing pins the histogram count.
//
// A turn the provider failed reaches OnModelEnd with a nil ModelResult. The
// bridge used to return before recording a duration, so model.duration.count
// silently drifted below model.calls — and a mean latency divided by the wrong
// denominator is the kind of wrong that never looks wrong.
func TestModelDurationCountsTurnsThatReturnedNothing(t *testing.T) {
	reader, mp := newMeter()
	obs := New(nil, WithMeterProvider(mp))

	ok := agent.ModelInfo{TaskID: "t", AgentName: "bot", SpanID: "s1", Model: "m"}
	obs.OnModelStart(context.Background(), ok)
	obs.OnModelEnd(context.Background(), ok, &agent.ModelResult{DurationMs: 120}, nil)

	failed := agent.ModelInfo{TaskID: "t", AgentName: "bot", SpanID: "s2", Model: "m"}
	obs.OnModelStart(context.Background(), failed)
	obs.OnModelEnd(context.Background(), failed, nil, errors.New("502 bad gateway"))

	rm := collect(t, reader)
	calls := mustSum(t, rm, "agentgo.model.calls")
	dur := mustSum(t, rm, "agentgo.model.duration")
	if calls != 2 {
		t.Fatalf("model.calls = %v, want 2", calls)
	}
	if dur != calls {
		t.Errorf("model.duration count = %v, model.calls = %v; every turn must be timed", dur, calls)
	}
	if !hasAttr(attrsOf(rm, "agentgo.model.calls"), mAttrStatus, statusError) {
		t.Error("the failed turn was not counted with status=error")
	}
}

// countingObserver records every model turn the runtime reported, so a test can
// compare the bridge's totals against the callbacks that produced them.
type countingObserver struct {
	agent.BaseObserver
	mu     sync.Mutex
	turns  int
	tokens int
}

func (c *countingObserver) OnModelEnd(_ context.Context, _ agent.ModelInfo, res *agent.ModelResult, _ error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns++
	if res != nil {
		c.tokens += res.TokensUsed
	}
}

func (c *countingObserver) snapshot() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turns, c.tokens
}

// TestTokenMetricsCoverTheForcedSynthesisTurn is the cross-check the live
// probe exists for, reduced to a scripted run.
//
// A run that hits its round ceiling ends with forceFinalSynthesis: one more
// model call, whose tokens and cost go into the budget and therefore into
// ExecutionResult. That call emitted no observer callback at all, so the
// bridge's token counters came up short by exactly one turn on precisely the
// runs a long-horizon operator is measuring.
func TestTokenMetricsCoverTheForcedSynthesisTurn(t *testing.T) {
	reader, mp := newMeter()
	counter := &countingObserver{}
	obs := New(nil, WithMeterProvider(mp))

	// A named, priced model, so the cost counter has something to say. An
	// injected generator that answers neither question is the default, and it
	// leaves agentgo.model empty and every turn unpriced — see namedGenerator.
	const model = "otel-crosscheck-model"
	pool.RegisterModelPricing(model, pool.ModelPricing{InputPer1K: 0.001, OutputPer1K: 0.002})
	t.Cleanup(func() { pool.UnregisterModelPricing(model) })

	llm := namedGenerator{Generator: extensiontest.Script(
		extensiontest.CallTool("echo", map[string]interface{}{"text": "hi"}),
		extensiontest.Answer("as far as I got"),
	), model: model}
	svc, err := extensiontest.NewServiceWithBuilder(
		agent.New("otel-bot").WithLLM(llm).WithObserver(obs, counter).WithExtensions(extensiontest.EchoTool()),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// One round, so the loop runs out of budget and has to synthesise.
	res, err := svc.Run(context.Background(), "say hi", agent.WithMaxTurns(1))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StopReason != agent.StopReasonMaxTurns {
		t.Fatalf("stop reason = %q, want max_turns (the synthesis path did not run)", res.StopReason)
	}

	turns, observedTokens := counter.snapshot()
	if turns < 2 {
		t.Fatalf("observers saw %d model turns; the forced synthesis turn is missing", turns)
	}

	rm := collect(t, reader)
	if got := mustSum(t, rm, "agentgo.model.calls"); int(got) != turns {
		t.Errorf("model.calls = %v, OnModelEnd calls = %d", got, turns)
	}
	if got := mustSum(t, rm, "agentgo.model.duration"); int(got) != turns {
		t.Errorf("model.duration count = %v, OnModelEnd calls = %d", got, turns)
	}
	prompt := mustSum(t, rm, "agentgo.tokens.prompt")
	completion := mustSum(t, rm, "agentgo.tokens.completion")
	if int(prompt+completion) != observedTokens {
		t.Errorf("tokens.prompt+completion = %v, observed TokensUsed = %d", prompt+completion, observedTokens)
	}

	// The run's own accounting is the independent second opinion, and cost is
	// the field to compare against: EstimatedCostUSD is read off the budget,
	// which the synthesis pass writes into, while ExecutionResult.EstimatedTokens
	// is summed from the per-round llm_latency events — a quantity the
	// synthesis turn is deliberately outside of, and therefore not a yardstick
	// for whether the bridge saw every turn.
	cost := mustSum(t, rm, "agentgo.cost.usd")
	if diff := cost - res.EstimatedCostUSD; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost.usd = %.10f, ExecutionResult.EstimatedCostUSD = %.10f", cost, res.EstimatedCostUSD)
	}
	if cost <= 0 {
		t.Error("the priced path recorded nothing; the model identity never reached the bridge")
	}
	if _, recorded := sumOf(rm, "agentgo.model.unpriced_turns"); recorded {
		t.Error("a priced model still counted unpriced turns")
	}
}

// namedGenerator gives an injected generator the model identity Builder.WithLLM
// looks for (GetModelName / GetBaseURL). Without it Service.Info().Model is
// empty, which is the name the runtime prices every turn with and the one the
// bridge stamps on every metric.
type namedGenerator struct {
	domain.Generator
	model string
}

func (g namedGenerator) GetModelName() string { return g.model }
func (g namedGenerator) GetBaseURL() string   { return "" }

// flakyLLM fails its first tool turn with a transient error and then behaves.
type flakyLLM struct {
	*extensiontest.ScriptedLLM
	mu     sync.Mutex
	failed bool
}

func (f *flakyLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	f.mu.Lock()
	first := !f.failed
	f.failed = true
	f.mu.Unlock()
	if first {
		return domain.ErrServiceUnavailable
	}
	return f.ScriptedLLM.StreamWithTools(ctx, messages, tools, opts, cb)
}

// TestRetryEventLandsOnItsModelSpan pins ModelRetryInfo.SpanID.
//
// The field is documented as "matches the OnModelStart / OnModelEnd pair this
// retry happened inside", and neither emitter filled it in — so every retry
// event fell back to the run's root and a turn that took three attempts still
// looked, on the timeline, exactly like one that took one.
func TestRetryEventLandsOnItsModelSpan(t *testing.T) {
	sr, tp := newRecorder()
	obs := New(tp)

	llm := &flakyLLM{ScriptedLLM: extensiontest.Script(extensiontest.Answer("recovered"))}
	svc, err := extensiontest.NewServiceWithBuilder(
		agent.New("retry-bot").WithLLM(llm).WithObserver(obs),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	if _, err := svc.Run(context.Background(), "answer briefly"); err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = obs.Shutdown(context.Background())

	var carriers []sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if hasEvent(s, "model.retry") {
			carriers = append(carriers, s)
		}
	}
	if len(carriers) == 0 {
		t.Fatal("no model.retry event was recorded on any span")
	}
	for _, s := range carriers {
		if kind := attrMap(s)[attrSpanKind].AsString(); kind != spanKindLLM {
			t.Errorf("model.retry landed on a %s span (%q), want the LLM turn it happened inside", kind, s.Name())
		}
		attrs := eventAttrs(s, "model.retry")
		if attrs["retry.kind"].AsString() != "transient_error" {
			t.Errorf("retry.kind = %q, want transient_error", attrs["retry.kind"].AsString())
		}
	}
}
