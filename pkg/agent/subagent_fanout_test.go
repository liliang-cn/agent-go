package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// fanOutLLM is a scripted provider whose every model turn costs a fixed
// latency, so the wall clock of a run is a direct reading of how many turns
// ran one after another.
//
// It tells the parent from a child by the sub-agent rules appended to every
// child's system prompt, and a parent's first turn from its second by whether
// any tool result has come back yet.
type fanOutLLM struct {
	latency  time.Duration
	children int // task calls the parent issues on its first turn
	agent    string

	// childCallsTask makes every child try to hand its work on with `task`,
	// which is what the depth limit exists to stop.
	childCallsTask bool
	// childChunks streams a child's final answer as this many partial
	// deltas before the answer itself.
	childChunks int

	mu         sync.Mutex
	childCalls []fanOutCall
	parentOpts []domain.GenerationOptions
	childTools [][]string
	toolTexts  []string
}

type fanOutCall struct {
	model    string
	provider string
	prompt   string
	start    time.Time
	end      time.Time
}

func (f *fanOutLLM) Generate(ctx context.Context, prompt string, opts *domain.GenerationOptions) (string, error) {
	return "done", nil
}

func (f *fanOutLLM) Stream(ctx context.Context, prompt string, opts *domain.GenerationOptions, cb func(string)) error {
	return nil
}

func (f *fanOutLLM) GenerateWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return f.turn(ctx, messages, tools, opts)
}

func (f *fanOutLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := f.turn(ctx, messages, tools, opts)
	if err != nil {
		return err
	}
	if f.childChunks > 0 && strings.HasPrefix(res.Content, "child finished") {
		for i := 0; i < f.childChunks; i++ {
			if err := cb(&domain.GenerationResult{Content: "."}); err != nil {
				return err
			}
		}
	}
	return cb(res)
}

func (f *fanOutLLM) GenerateStructured(ctx context.Context, prompt string, schema interface{}, opts *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Raw: `{"forbid_tools":false,"deliverables":[]}`, Valid: true}, nil
}

func (f *fanOutLLM) RecognizeIntent(ctx context.Context, request string) (*domain.IntentResult, error) {
	return nil, nil
}

func (f *fanOutLLM) turn(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions) (*domain.GenerationResult, error) {
	start := time.Now()
	select {
	case <-time.After(f.latency):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	end := time.Now()

	system := ""
	if len(messages) > 0 && messages[0].Role == "system" {
		system = messages[0].Content
	}
	var toolReplies []string
	for _, m := range messages {
		if m.Role == "tool" {
			toolReplies = append(toolReplies, m.Content)
		}
	}
	var o domain.GenerationOptions
	if opts != nil {
		o = *opts
	}

	if strings.Contains(system, "Sub-Agent Execution Rules") {
		prompt := ""
		for _, m := range messages {
			if m.Role == "user" && !strings.Contains(m.Content, "<system-reminder>") {
				prompt = m.Content
			}
		}
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Function.Name)
		}
		f.mu.Lock()
		f.childCalls = append(f.childCalls, fanOutCall{model: o.Model, provider: o.Provider, prompt: prompt, start: start, end: end})
		f.childTools = append(f.childTools, names)
		f.toolTexts = append(f.toolTexts, toolReplies...)
		f.mu.Unlock()

		if f.childCallsTask && len(toolReplies) == 0 {
			return &domain.GenerationResult{ToolCalls: []domain.ToolCall{{
				ID:   "grandchild",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "task",
					Arguments: map[string]interface{}{"agent_name": f.agent, "prompt": "go one level deeper"},
				},
			}}}, nil
		}
		return &domain.GenerationResult{Content: "child finished: " + prompt}, nil
	}

	f.mu.Lock()
	f.parentOpts = append(f.parentOpts, o)
	f.mu.Unlock()
	if len(toolReplies) == 0 {
		calls := make([]domain.ToolCall, 0, f.children)
		for i := 0; i < f.children; i++ {
			calls = append(calls, domain.ToolCall{
				ID:   fmt.Sprintf("task_%d", i),
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "task",
					Arguments: map[string]interface{}{"agent_name": f.agent, "prompt": fmt.Sprintf("part %d", i)},
				},
			})
		}
		return &domain.GenerationResult{ToolCalls: calls}, nil
	}
	return &domain.GenerationResult{Content: fmt.Sprintf("combined %d answers", len(toolReplies))}, nil
}

// fanOutSpan is the time from the first child turn starting to the last one
// ending — the part of the run that fan-out changes.
func (f *fanOutLLM) fanOutSpan() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.childCalls) == 0 {
		return 0
	}
	first, last := f.childCalls[0].start, f.childCalls[0].end
	for _, c := range f.childCalls {
		if c.start.Before(first) {
			first = c.start
		}
		if c.end.After(last) {
			last = c.end
		}
	}
	return last.Sub(first)
}

func newFanOutService(t *testing.T, llm *fanOutLLM, spec SubagentSpec, configure func(*Builder) *Builder) *Service {
	t.Helper()
	b := New("lead").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithSubagents(spec)
	if configure != nil {
		b = configure(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func runFanOut(t *testing.T, parallel bool) (total, span time.Duration, llm *fanOutLLM, res *ExecutionResult) {
	t.Helper()
	llm = &fanOutLLM{latency: 300 * time.Millisecond, children: 4, agent: "reader"}
	spec := SubagentSpec{Name: "reader", Description: "reads things", Instructions: "Read and report.", MaxTurns: 3}
	spec.Parallel = parallel
	svc := newFanOutService(t, llm, spec, nil)

	started := time.Now()
	res, err := svc.Run(context.Background(), "Read four things.", WithConstraintExtraction(false))
	total = time.Since(started)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return total, llm.fanOutSpan(), llm, res
}

// Four task calls in one turn, every model turn 300ms. Serial is four child
// turns end to end; parallel is all four at once.
func TestSubagentFanOutWallClock(t *testing.T) {
	serialTotal, serialSpan, serialLLM, _ := runFanOut(t, false)
	parallelTotal, parallelSpan, parallelLLM, res := runFanOut(t, true)

	t.Logf("serial:   total=%v fan-out=%v child turns=%d", serialTotal.Round(time.Millisecond), serialSpan.Round(time.Millisecond), len(serialLLM.childCalls))
	t.Logf("parallel: total=%v fan-out=%v child turns=%d", parallelTotal.Round(time.Millisecond), parallelSpan.Round(time.Millisecond), len(parallelLLM.childCalls))

	if len(serialLLM.childCalls) != 4 || len(parallelLLM.childCalls) != 4 {
		t.Fatalf("each run should make four child turns, got serial=%d parallel=%d", len(serialLLM.childCalls), len(parallelLLM.childCalls))
	}
	if serialSpan < 1100*time.Millisecond {
		t.Errorf("a spec that did not declare Parallel must run serially; fan-out took %v", serialSpan)
	}
	if parallelSpan > 700*time.Millisecond {
		t.Errorf("four parallel-safe children should overlap; fan-out took %v", parallelSpan)
	}
	if !strings.Contains(res.Text(), "combined 4 answers") {
		t.Errorf("parent should have seen all four results, got %q", res.Text())
	}
}

// The per-turn cap: six parallel-safe calls with room for two run as three
// waves, and never more than two children are inside a model turn at once.
func TestSubagentParallelismIsBounded(t *testing.T) {
	llm := &fanOutLLM{latency: 200 * time.Millisecond, children: 6, agent: "reader"}
	spec := SubagentSpec{Name: "reader", Description: "reads", Instructions: "Read.", MaxTurns: 3, Parallel: true}
	svc := newFanOutService(t, llm, spec, func(b *Builder) *Builder { return b.WithSubagentMaxParallel(2) })
	if got := svc.SubagentMaxParallel(); got != 2 {
		t.Fatalf("SubagentMaxParallel() = %d, want 2", got)
	}

	if _, err := svc.Run(context.Background(), "Read six things.", WithConstraintExtraction(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	peak := llm.peakConcurrentChildren()
	span := llm.fanOutSpan()
	t.Logf("6 children, cap 2: fan-out=%v peak concurrent=%d", span.Round(time.Millisecond), peak)
	if peak != 2 {
		t.Errorf("peak concurrent children = %d, want exactly the cap of 2", peak)
	}
	if span < 550*time.Millisecond {
		t.Errorf("six children two at a time is three waves of 200ms; fan-out took %v", span)
	}
}

// peakConcurrentChildren is the most child turns that overlapped in time.
func (f *fanOutLLM) peakConcurrentChildren() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	peak := 0
	for _, a := range f.childCalls {
		n := 0
		for _, b := range f.childCalls {
			if !b.start.After(a.start) && b.end.After(a.start) {
				n++
			}
		}
		if n > peak {
			peak = n
		}
	}
	return peak
}

// At the default depth a child is not offered `task`, and a `task` call it
// makes anyway is answered with a refusal it can read — the run carries on
// and no grandchild is started.
func TestSubagentDepthLimitRefusesAndWithholds(t *testing.T) {
	llm := &fanOutLLM{latency: time.Millisecond, children: 1, agent: "reader", childCallsTask: true}
	spec := SubagentSpec{Name: "reader", Description: "reads", Instructions: "Read.", MaxTurns: 3}
	svc := newFanOutService(t, llm, spec, nil)
	if got := svc.SubagentMaxDepth(); got != DefaultSubagentMaxDepth {
		t.Fatalf("SubagentMaxDepth() = %d, want default %d", got, DefaultSubagentMaxDepth)
	}

	res, err := svc.Run(context.Background(), "Read one thing.", WithConstraintExtraction(false))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatalf("a refused delegation must not end the run; got %+v", res)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	for i, names := range llm.childTools {
		for _, n := range names {
			for _, d := range delegationToolNames {
				if n == d {
					t.Errorf("child turn %d at the depth limit was offered %q", i, n)
				}
			}
		}
	}
	for _, c := range llm.childCalls {
		if strings.Contains(c.prompt, "go one level deeper") {
			t.Fatalf("a grandchild ran past the depth limit")
		}
	}
	refused := false
	for _, txt := range llm.toolTexts {
		if strings.Contains(txt, "subagent_depth_limit") && strings.Contains(txt, "yourself") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the child never saw a structured depth refusal; tool replies: %v", llm.toolTexts)
	}
}

// Raising the bound lets exactly one more level through, and the level at the
// new bound is the one that loses the tool.
func TestSubagentMaxDepthTwoAllowsOneMoreLevel(t *testing.T) {
	llm := &fanOutLLM{latency: time.Millisecond, children: 1, agent: "reader", childCallsTask: true}
	spec := SubagentSpec{Name: "reader", Description: "reads", Instructions: "Read.", MaxTurns: 3}
	svc := newFanOutService(t, llm, spec, func(b *Builder) *Builder { return b.WithSubagentMaxDepth(2) })

	if _, err := svc.Run(context.Background(), "Read one thing.", WithConstraintExtraction(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	grandchild, childOffered, grandchildOffered := false, false, false
	for i, c := range llm.childCalls {
		deep := strings.Contains(c.prompt, "go one level deeper")
		offered := containsName(llm.childTools[i], "task")
		if deep {
			grandchild = true
			grandchildOffered = grandchildOffered || offered
		} else {
			childOffered = childOffered || offered
		}
	}
	if !grandchild {
		t.Fatal("with max depth 2 the child's delegation should have run")
	}
	if !childOffered {
		t.Error("a depth-1 child under max depth 2 should be offered task")
	}
	if grandchildOffered {
		t.Error("the depth-2 grandchild sits at the bound and must not be offered task")
	}
}

// The spec's model reaches the child's requests through the run's own model
// field, and the parent's requests are untouched.
func TestSubagentSpecModelRoutesTheChildsRequests(t *testing.T) {
	llm := &fanOutLLM{latency: time.Millisecond, children: 2, agent: "cheap"}
	spec := SubagentSpec{Name: "cheap", Description: "cheap", Instructions: "Answer.", MaxTurns: 3,
		Model: "small-model", Provider: "provider-b", Parallel: true}
	svc := newFanOutService(t, llm, spec, nil)

	if _, err := svc.Run(context.Background(), "Two lookups.", WithConstraintExtraction(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.childCalls) != 2 {
		t.Fatalf("child turns = %d, want 2", len(llm.childCalls))
	}
	for i, c := range llm.childCalls {
		t.Logf("child turn %d: model=%q provider=%q", i, c.model, c.provider)
		if c.model != "small-model" || c.provider != "provider-b" {
			t.Errorf("child turn %d asked for %q/%q, want small-model/provider-b", i, c.model, c.provider)
		}
	}
	for i, o := range llm.parentOpts {
		t.Logf("parent turn %d: model=%q provider=%q", i, o.Model, o.Provider)
		if o.Model != "" || o.Provider != "" {
			t.Errorf("parent turn %d was routed to %q/%q; only the child should be", i, o.Model, o.Provider)
		}
	}
}

// Parallel children interleave on one channel. Every event they send must
// arrive, stamped with the child that sent it, in the order that child sent
// it — even when the consumer is slower than they are.
func TestParallelSubagentEventsArriveWholeAndAttributed(t *testing.T) {
	const chunks = 150 // well past the 64-event buffer each hop has
	llm := &fanOutLLM{latency: 5 * time.Millisecond, children: 4, agent: "reader", childChunks: chunks}
	spec := SubagentSpec{Name: "reader", Description: "reads", Instructions: "Read.", MaxTurns: 3, Parallel: true}
	svc := newFanOutService(t, llm, spec, nil)

	events, err := svc.RunStreamWithOptions(context.Background(), "Read four things.", WithConstraintExtraction(false))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	partials := map[string]int{}
	finishedLast := map[string]bool{}
	for evt := range events {
		time.Sleep(50 * time.Microsecond) // a consumer slower than the producers
		if evt.SubAgentID == "" {
			continue
		}
		if evt.SubAgentDepth != 1 {
			t.Errorf("child event depth = %d, want 1", evt.SubAgentDepth)
		}
		// A child's own stream is in order: none of its answer arrives after
		// its run reported completing.
		if finishedLast[evt.SubAgentID] && evt.Type == EventTypePartial {
			t.Errorf("child %s streamed a partial after it reported completion", evt.SubAgentID)
		}
		switch {
		case evt.Type == EventTypePartial:
			partials[evt.SubAgentID]++
		case evt.Type == EventTypeStateUpdate && evt.Content == "Delegated step completed":
			finishedLast[evt.SubAgentID] = true
		}
	}
	if len(partials) != 4 {
		t.Fatalf("partial events came from %d distinct children, want 4", len(partials))
	}
	for id, n := range partials {
		if n < chunks {
			t.Errorf("child %s: %d partial events arrived, want at least %d", id, n, chunks)
		}
		if !finishedLast[id] {
			t.Errorf("child %s never reported completion", id)
		}
	}
}

// The streaming gate in isolation: safe calls overlap, an exclusive call
// waits for all of them, and a later safe call waits for the exclusive one.
func TestToolCallGateOrdersByDeclaration(t *testing.T) {
	g := newToolCallGate(4)
	a := g.admit(true, false)
	b := g.admit(true, false)
	w := g.admit(false, false)
	c := g.admit(true, false)
	ctx := context.Background()

	if len(a.waitFor) != 0 || len(b.waitFor) != 0 {
		t.Fatalf("leading safe calls should not wait: %d %d", len(a.waitFor), len(b.waitFor))
	}
	started := make(chan string, 4)
	go func() { _ = w.wait(ctx); started <- "write"; w.finish() }()
	go func() { _ = c.wait(ctx); started <- "read-after"; c.finish() }()

	select {
	case s := <-started:
		t.Fatalf("%s started while earlier safe calls were still running", s)
	case <-time.After(30 * time.Millisecond):
	}
	a.finish()
	b.finish()
	if first := <-started; first != "write" {
		t.Fatalf("first to start after the reads = %q, want the write", first)
	}
	if second := <-started; second != "read-after" {
		t.Fatalf("second = %q", second)
	}

	cancelled, cancel := context.WithCancel(ctx)
	blocker := g.admit(false, false)
	stuck := g.admit(false, false)
	cancel()
	if err := stuck.wait(cancelled); err == nil {
		t.Fatal("a call still waiting when the run stops must not start")
	}
	blocker.finish()
	stuck.finish()
}
