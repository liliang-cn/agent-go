package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// syntheticSource is tool output shaped like a source file: varied lines, so
// the tokenizer sees something close to a real read rather than one repeated
// word.
func syntheticSource(seed, chars int) string {
	var b strings.Builder
	for j := 0; b.Len() < chars; j++ {
		fmt.Fprintf(&b, "func handler%d_%d(ctx context.Context, req *Request) error { return process(ctx, req, %d) }\n", seed, j, seed*j)
	}
	return b.String()
}

// toolHeavyHistory is a coding agent's history: the goal, then one fs_read
// per round with a large result and a short note after it.
func toolHeavyHistory(rounds, resultChars int) []domain.Message {
	msgs := []domain.Message{{Role: "user", Content: "Find the root cause in app.log and make go test ./... pass."}}
	for i := 1; i <= rounds; i++ {
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			domain.Message{Role: "assistant", ToolCalls: []domain.ToolCall{{
				ID: id, Type: "function",
				Function: domain.FunctionCall{Name: "fs_read", Arguments: map[string]interface{}{
					"path": fmt.Sprintf("pkg/file%02d.go", i), "offset": float64(0)}},
			}}},
			domain.Message{Role: "tool", ToolCallID: id, Content: syntheticSource(i, resultChars)},
			domain.Message{Role: "assistant", Content: fmt.Sprintf("file%02d.go does not contain it.", i)},
		)
	}
	return msgs
}

func TestClipOldToolResultsKeepsPairingAndRecentRounds(t *testing.T) {
	in := toolHeavyHistory(10, 5000)
	orig := append([]domain.Message(nil), in...)
	out, n := clipOldToolResults(in, protectedHeadEnd(in), 4)

	if n != 6 {
		t.Fatalf("clipped %d results, want the 6 older than the last 4 rounds", n)
	}
	if len(out) != len(in) {
		t.Fatalf("clipping changed the message count %d -> %d", len(in), len(out))
	}
	for i := range in {
		if in[i].Content != orig[i].Content {
			t.Fatal("clipping modified its input slice")
		}
		if out[i].Role != in[i].Role || out[i].ToolCallID != in[i].ToolCallID || len(out[i].ToolCalls) != len(in[i].ToolCalls) {
			t.Fatalf("message %d changed shape: %+v -> %+v", i, in[i], out[i])
		}
	}
	// Every call still has exactly one result, in place.
	if got := sanitizeForTest(out); got != len(out) {
		t.Fatalf("pairing broken: %d of %d messages survive a pairing check", got, len(out))
	}
	// Rounds 1..6 are stubs, 7..10 verbatim.
	for i := 1; i <= 10; i++ {
		tool := out[3*i-1]
		stub := strings.HasPrefix(tool.Content, clipStubMarker)
		if i <= 6 && !stub {
			t.Errorf("round %d should be clipped", i)
		}
		if i > 6 && stub {
			t.Errorf("round %d is recent and must stay verbatim", i)
		}
	}
	stub := out[2].Content
	for _, want := range []string{"fs_read(", `path="pkg/file01.go"`, "5", "lines", "First:", "Last:", "again"} {
		if !strings.Contains(stub, want) {
			t.Errorf("stub %q is missing %q", stub, want)
		}
	}
	// A stub is never clipped again, and the same history clips to the same
	// bytes: what a prompt cache needs from here on.
	again, n2 := clipOldToolResults(out, protectedHeadEnd(out), 4)
	if n2 != 0 {
		t.Fatalf("re-clipping replaced %d stubs", n2)
	}
	for i := range out {
		if again[i].Content != out[i].Content {
			t.Fatal("re-clipping is not byte-stable")
		}
	}
}

// sanitizeForTest counts messages whose tool pairing is intact: every tool
// result answers a call made earlier, every call gets exactly one result.
func sanitizeForTest(msgs []domain.Message) int {
	open := map[string]int{}
	ok := 0
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			open[tc.ID]++
		}
		if m.Role == "tool" {
			if open[m.ToolCallID] != 1 {
				continue
			}
			open[m.ToolCallID]--
		}
		ok++
	}
	return ok
}

func TestClipOldToolResultsProtectsTheGoal(t *testing.T) {
	msgs := []domain.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("the goal is long ", 200)},
		{Role: "assistant", Content: "ok"},
	}
	if end := protectedHeadEnd(msgs); end != 2 {
		t.Fatalf("protected head ends at %d, want system + first user = 2", end)
	}
	headEnd, _ := compactionBounds(toolHeavyHistory(10, 100), 6)
	if headEnd != 1 {
		t.Fatalf("goal not in the protected head: headEnd=%d", headEnd)
	}
}

func TestCompactionThresholdForWindow(t *testing.T) {
	cases := []struct {
		window, out, want int
	}{
		{200000, 8192, 95904}, // large: 50% of usable
		{128000, 8192, 64000}, // mid: the 64k floor
		{32768, 8192, 19660},  // small: 80% of usable
		{1048576, 65536, 491520},
		{0, 8192, 0}, // unknown
	}
	for _, c := range cases {
		if got := CompactionThresholdForWindow(c.window, c.out); got != c.want {
			t.Errorf("window %d out %d: got %d, want %d", c.window, c.out, got, c.want)
		}
	}
	// Continuous: a smaller window never compacts later than a larger one.
	prev := 0
	for w := 8000; w <= 400000; w += 1000 {
		got := CompactionThresholdForWindow(w, 8192)
		if got < prev {
			t.Fatalf("threshold fell from %d to %d at window %d", prev, got, w)
		}
		prev = got
	}
}

func TestResolveCompactionThresholdSources(t *testing.T) {
	if n, src := resolveCompactionThreshold(12345, "gpt-4o", 8192); n != 12345 || src != CompactionThresholdConfigured {
		t.Fatalf("explicit threshold = %d %s", n, src)
	}
	if n, src := resolveCompactionThreshold(0, "gpt-4o", 8192); src != CompactionThresholdModelWindow || n != CompactionThresholdForWindow(128000, 8192) {
		t.Fatalf("known window = %d %s", n, src)
	}
	if n, src := resolveCompactionThreshold(0, "some-gateway-alias", 8192); n != CompactionDefaultThresholdTokens || src != CompactionThresholdDefault {
		t.Fatalf("unknown window = %d %s, want the default and a source that says so", n, src)
	}
}

func TestDoctorWarnsWhenAModelWindowIsUnknown(t *testing.T) {
	home := healthyHome(t)
	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	got := findCheck(t, report, "llm.provider.local.window")
	if got.Status != DoctorWarn || !strings.Contains(got.Detail, "test-model") {
		t.Fatalf("window check = %v %q, want a warning naming the model", got.Status, got.Detail)
	}
	pool.RegisterModelWindow("test-model", pool.ModelWindow{ContextTokens: 200000})
	defer pool.UnregisterModelWindow("test-model")
	report, err = Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if got := findCheck(t, report, "llm.provider.local.window"); got.Status != DoctorOK {
		t.Fatalf("window check after registering = %v %q", got.Status, got.Detail)
	}
}

func TestBuildFallbackSummary(t *testing.T) {
	middle := []domain.Message{
		{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: "a", Function: domain.FunctionCall{
			Name: "fs_write", Arguments: map[string]interface{}{"path": "fix.go", "content": strings.Repeat("x", 5000)}}}}},
		{Role: "tool", ToolCallID: "a", Content: "wrote 5000 bytes"},
		{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: "b", Function: domain.FunctionCall{
			Name: "bash", Arguments: map[string]interface{}{"command": "go test ./..."}}}}},
		{Role: "tool", ToolCallID: "b", Content: "FAIL\nError: exit status 1"},
	}
	got := buildFallbackSummary(fallbackSummaryInput{
		Goal: "fix the test", Plan: "- [x] find root cause (note: nil map in cache.go)", Middle: middle, Budget: 2000,
	})
	for _, want := range []string{"fix the test", "nil map in cache.go", "fix.go", "Error: exit status 1", "bash(", "fs_write("} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback summary is missing %q:\n%s", want, got)
		}
	}
	if len(got) > 2000 {
		t.Errorf("fallback summary is %d chars, over its 2000 budget", len(got))
	}
	if strings.Contains(got, strings.Repeat("x", 100)) {
		t.Error("a write's content leaked into the summary")
	}
}

// ---- Measurement 1: mechanical clipping on a long tool-heavy history ----

// TestMeasureClippingOnToolHeavyHistory sizes a 30-round coding history
// before and after clipping. Run with -v for the numbers.
func TestMeasureClippingOnToolHeavyHistory(t *testing.T) {
	tc := pool.NewTokenCounter()
	msgs := toolHeavyHistory(30, 6000)
	before := tc.EstimateConversationTokens(msgs, "")
	out, n := clipOldToolResults(msgs, protectedHeadEnd(msgs), CompactionDefaultClipAfterRounds)
	after := tc.EstimateConversationTokens(out, "")
	t.Logf("MEASURE clip: 30 rounds x 6000-char results: %d -> %d tokens (%.1f%% removed), %d results stubbed",
		before, after, 100*float64(before-after)/float64(before), n)
	if after >= before/4 {
		t.Fatalf("clipping removed too little: %d -> %d", before, after)
	}
}

// compactLoopLLM drives a coding-shaped run through the real loop: `rounds`
// reads with large results, then a final answer. Summary calls go through
// Generate and are counted; failSummary makes every one of them fail.
type compactLoopLLM struct {
	rounds       int
	resultChars  int
	failSummary  bool
	reportPrompt int // provider-reported prompt tokens per turn; 0 = none

	mu           sync.Mutex
	turns        int
	summaryCalls int32
}

func (l *compactLoopLLM) reply() *domain.GenerationResult {
	l.mu.Lock()
	l.turns++
	n := l.turns
	l.mu.Unlock()
	var usage *domain.TokenUsage
	if l.reportPrompt > 0 {
		usage = &domain.TokenUsage{PromptTokens: l.reportPrompt, CompletionTokens: 20}
	}
	if n > l.rounds {
		return &domain.GenerationResult{Content: "The root cause is a nil map in cache.go; fixed and tests pass.", FinishReason: "stop", Usage: usage}
	}
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("call-%d", n), Type: "function",
			Function: domain.FunctionCall{Name: "read_source", Arguments: map[string]interface{}{"path": fmt.Sprintf("pkg/file%02d.go", n)}},
		}},
		FinishReason: "tool_calls",
		Usage:        usage,
	}
}

func (l *compactLoopLLM) Generate(_ context.Context, prompt string, _ *domain.GenerationOptions) (string, error) {
	if strings.Contains(prompt, "Summarize the following conversation slice") {
		atomic.AddInt32(&l.summaryCalls, 1)
		if l.failSummary {
			return "", errors.New("summariser unavailable: 502 bad gateway")
		}
		return "- read several files; none contained the bug", nil
	}
	return "", nil
}
func (l *compactLoopLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *compactLoopLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(), nil
}
func (l *compactLoopLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply())
}
func (l *compactLoopLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *compactLoopLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

type compactLoopOutcome struct {
	result       *ExecutionResult
	compactions  []CompactionInfo
	summaryCalls int
	reads        int32
}

func runCompactLoop(t *testing.T, llm *compactLoopLLM, opts ...RunOption) compactLoopOutcome {
	t.Helper()
	obs := &recordingCompactionObserver{}
	svc, err := New("compaction-measure").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		WithObserver(obs).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	var reads int32
	svc.AddToolWithMetadata("read_source", "Reads a source file.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"}}},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			n := atomic.AddInt32(&reads, 1)
			return syntheticSource(int(n), llm.resultChars), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})

	all := append([]RunOption{WithConstraintExtraction(false), WithMaxTurns(llm.rounds + 10)}, opts...)
	res, err := svc.Run(context.Background(), "Find the root cause and make the tests pass.", all...)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	return compactLoopOutcome{
		result:       res,
		compactions:  append([]CompactionInfo(nil), obs.seen...),
		summaryCalls: int(atomic.LoadInt32(&llm.summaryCalls)),
		reads:        atomic.LoadInt32(&reads),
	}
}

func modeCounts(infos []CompactionInfo) map[string]int {
	out := map[string]int{}
	for _, i := range infos {
		out[i.Mode]++
	}
	return out
}

func maxContextAfter(infos []CompactionInfo) int {
	m := 0
	for _, i := range infos {
		if i.ContextTokensAfter > m {
			m = i.ContextTokensAfter
		}
	}
	return m
}

// TestMeasureSummaryCallsWithAndWithoutClipping runs the same 30-round
// tool-heavy task through the loop twice — clipping off (the old behaviour)
// and on — and counts summariser calls. Run with -v for the numbers.
func TestMeasureSummaryCallsWithAndWithoutClipping(t *testing.T) {
	const rounds, chars, threshold = 30, 6000, 12000

	off := runCompactLoop(t, &compactLoopLLM{rounds: rounds, resultChars: chars},
		WithAutoCompaction(threshold, 0), WithCompactionClipping(-1))
	on := runCompactLoop(t, &compactLoopLLM{rounds: rounds, resultChars: chars},
		WithAutoCompaction(threshold, 0))

	t.Logf("MEASURE loop %d rounds, %d-char results, threshold %d:", rounds, chars, threshold)
	t.Logf("MEASURE   clipping off: %d compactions %v, %d summary calls, max context after %d",
		len(off.compactions), modeCounts(off.compactions), off.summaryCalls, maxContextAfter(off.compactions))
	t.Logf("MEASURE   clipping on:  %d compactions %v, %d summary calls, max context after %d",
		len(on.compactions), modeCounts(on.compactions), on.summaryCalls, maxContextAfter(on.compactions))

	if !off.result.Success || !on.result.Success {
		t.Fatalf("runs did not complete: off=%v on=%v", off.result.Success, on.result.Success)
	}
	if off.summaryCalls == 0 {
		t.Fatal("baseline never summarised; the scenario is not exercising compaction")
	}
	if on.summaryCalls >= off.summaryCalls {
		t.Fatalf("clipping did not reduce summary calls: %d -> %d", off.summaryCalls, on.summaryCalls)
	}
	if modeCounts(on.compactions)[CompactionModeClip] == 0 {
		t.Fatal("no compaction was satisfied by clipping alone")
	}
	for _, c := range on.compactions {
		if c.ThresholdSource != CompactionThresholdConfigured || c.Threshold != threshold {
			t.Fatalf("compaction reported threshold %d/%s", c.Threshold, c.ThresholdSource)
		}
	}
}

// ---- Measurement 3: a summariser that always fails ----

func TestCompactionSurvivesASummariserThatAlwaysFails(t *testing.T) {
	const rounds, chars, threshold = 30, 6000, 3000
	llm := &compactLoopLLM{rounds: rounds, resultChars: chars, failSummary: true}
	out := runCompactLoop(t, llm, WithAutoCompaction(threshold, 0))

	modes := modeCounts(out.compactions)
	t.Logf("MEASURE failing summariser, %d rounds, threshold %d: %d compactions %v, %d summary calls, max context after %d, success=%v",
		rounds, threshold, len(out.compactions), modes, out.summaryCalls, maxContextAfter(out.compactions), out.result.Success)

	if !out.result.Success {
		t.Fatalf("the run did not terminate cleanly: %+v", out.result)
	}
	if modes[CompactionModeFallback] == 0 {
		t.Fatal("the history was never folded without the summariser")
	}
	degraded := 0
	for _, c := range out.compactions {
		if c.Degraded {
			degraded++
			if c.Mode != CompactionModeFallback {
				t.Errorf("degraded compaction reported mode %q", c.Mode)
			}
		}
	}
	if degraded != modes[CompactionModeFallback] {
		t.Errorf("degraded=%d but fallback mode=%d", degraded, modes[CompactionModeFallback])
	}
	// Two failures, then one attempt per cooldown window at most.
	bound := compactionFailuresBeforeFallback + (rounds+10)/compactionFailureCooldownRounds + 1
	if out.summaryCalls > bound {
		t.Fatalf("summariser called %d times over %d rounds; the cooldown should bound it to %d", out.summaryCalls, rounds, bound)
	}
	// And the context stays bounded: nothing after a fallback fold is
	// anywhere near the unfolded history.
	unbounded := pool.NewTokenCounter().EstimateTokens(syntheticSource(1, chars), "") * rounds
	if m := maxContextAfter(out.compactions); m > unbounded/3 {
		t.Fatalf("context after compaction reached %d of an unfolded %d", m, unbounded)
	}
}

// ---- Anchoring ----

// The loop sizes its history from the provider's report, not the estimate
// alone: a provider saying the prompt is 100k compacts a run whose messages
// estimate at a few hundred tokens.
func TestCompactionTriggerAnchorsOnReportedUsage(t *testing.T) {
	llm := &compactLoopLLM{rounds: 6, resultChars: 200, reportPrompt: 100000}
	out := runCompactLoop(t, llm, WithAutoCompaction(60000, 0))
	if len(out.compactions) == 0 {
		t.Fatal("reported usage above the threshold did not trigger compaction")
	}
	if got := out.compactions[0].ContextTokens; got < 100000 {
		t.Fatalf("compaction measured %d tokens, want the anchored >= 100000", got)
	}

	control := runCompactLoop(t, &compactLoopLLM{rounds: 6, resultChars: 200}, WithAutoCompaction(60000, 0))
	if len(control.compactions) != 0 {
		t.Fatalf("without a report the same run compacted %d times", len(control.compactions))
	}
}

// ---- Measurement 2: estimate error, pure vs anchored ----

// fixtureTools is a realistic tool surface: ten coding tools with described
// parameters, the part of every request the message slice does not contain.
func fixtureTools() []domain.ToolDefinition {
	names := []string{"fs_read", "fs_write", "fs_edit", "fs_list", "fs_grep", "bash", "plan_update", "plan_read", "task_complete", "task_blocked"}
	out := make([]domain.ToolDefinition, 0, len(names))
	for _, n := range names {
		out = append(out, domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{
			Name:        n,
			Description: "Operates on the workspace: " + n + ". Paths are relative to the sandbox root; large outputs are paginated, pass offset and limit to page through them.",
			Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string", "description": "Workspace-relative path of the file or directory."},
				"offset":  map[string]interface{}{"type": "integer", "description": "First line to return, 0-based."},
				"limit":   map[string]interface{}{"type": "integer", "description": "Maximum number of lines to return."},
				"content": map[string]interface{}{"type": "string", "description": "Text to write; replaces the file."},
			}, "required": []string{"path"}},
		}})
	}
	return out
}

// TestMeasureEstimateErrorPureVsAnchored replays a scripted run against a
// provider whose count differs from the local estimate the two ways real ones
// do: its tokenizer reads 18% more tokens than tiktoken for the same text, and
// every request carries a system prompt and tool schemas the loop's message
// slice does not contain. Before each turn the loop sizes its history; the
// error is against what the provider then reports for that turn. Three
// estimators: the old one (messages only), messages plus the request overhead
// (what the loop uses before any report), and anchored on the last report.
// Run with -v for the numbers.
func TestMeasureEstimateErrorPureVsAnchored(t *testing.T) {
	const (
		ratio  = 1.18
		rounds = 25
	)
	tc := pool.NewTokenCounter()
	system := domain.Message{Role: "system", Content: strings.Repeat(
		"You are a coding agent working in a sandboxed Go workspace. Read before you write, run the tests after every change, and record progress in the plan. ", 12)}
	tools := fixtureTools()
	reported := func(msgs []domain.Message) int {
		sent := append([]domain.Message{system}, msgs...)
		local := tc.EstimateConversationTokens(sent, "") + tc.EstimateToolsTokens(tools, "")
		return int(math.Round(ratio * float64(local)))
	}

	msgs := []domain.Message{{Role: "user", Content: "Find the root cause and make the tests pass."}}
	var anchor usageAnchor
	var oldErr, overheadErr, anchErr float64
	n := 0
	for i := 1; i <= rounds; i++ {
		sent := append([]domain.Message{system}, msgs...)
		// The loop's view just before turn i.
		old := tc.EstimateConversationTokens(msgs, "")
		withOverhead := old + requestOverheadTokens(tc, "", msgs, sent, tools)
		anch, anchored := anchoredTokens(tc, "", anchor, msgs)
		if !anchored {
			anch = withOverhead
		}
		actual := reported(msgs)
		if i > 1 { // turn 1 has no report to anchor on
			oldErr += math.Abs(float64(old-actual)) / float64(actual)
			overheadErr += math.Abs(float64(withOverhead-actual)) / float64(actual)
			anchErr += math.Abs(float64(anch-actual)) / float64(actual)
			n++
		}
		anchor = newUsageAnchor(actual, msgs)
		// The turn's reply and its tool result join the history.
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			domain.Message{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: id, Type: "function",
				Function: domain.FunctionCall{Name: "fs_read", Arguments: map[string]interface{}{"path": fmt.Sprintf("f%02d.go", i)}}}}},
			domain.Message{Role: "tool", ToolCallID: id, Content: syntheticSource(i, 1500+300*(i%5))},
		)
	}
	oldMean, ovMean, anchMean := oldErr/float64(n), overheadErr/float64(n), anchErr/float64(n)
	t.Logf("MEASURE estimate error over %d turns (provider x%.2f tokenizer, system %d + tools %d local tokens): messages-only %.1f%%, +system+tools %.1f%%, anchored %.1f%%",
		n, ratio, tc.EstimateConversationTokens([]domain.Message{system}, ""), tc.EstimateToolsTokens(tools, ""),
		100*oldMean, 100*ovMean, 100*anchMean)
	if ovMean >= oldMean {
		t.Fatalf("counting the system prompt and tools did not cut the error: %.3f -> %.3f", oldMean, ovMean)
	}
	if anchMean >= ovMean/3 {
		t.Fatalf("anchoring did not cut the error: overhead-only %.3f, anchored %.3f", ovMean, anchMean)
	}
}

// Without a provider report the loop still counts what it sends besides the
// messages: tool schemas big enough to cross the threshold trigger compaction
// on a history that alone is under it. The control is the same run without
// the extra tools.
func TestCompactionTriggerCountsToolSchemas(t *testing.T) {
	run := func(extraTools int) []CompactionInfo {
		llm := &compactLoopLLM{rounds: 6, resultChars: 200}
		obs := &recordingCompactionObserver{}
		svc, err := New("compaction-tools").
			WithConfig(testAgentConfig(t.TempDir())).
			WithLLM(llm).
			WithObserver(obs).
			Build()
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		defer svc.Close()
		svc.AddToolWithMetadata("read_source", "Reads a source file.",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"}}},
			func(context.Context, map[string]interface{}) (interface{}, error) {
				return syntheticSource(1, llm.resultChars), nil
			},
			ToolMetadata{ReadOnly: true, ConcurrencySafe: true})
		for i := 0; i < extraTools; i++ {
			svc.AddTool(fmt.Sprintf("extra_tool_%02d", i), strings.Repeat("A tool with a long, detailed description of its behaviour. ", 8),
				map[string]interface{}{"type": "object", "properties": map[string]interface{}{
					"arg": map[string]interface{}{"type": "string", "description": strings.Repeat("an argument ", 10)}}},
				func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil })
		}
		if _, err := svc.Run(context.Background(), "Find the root cause.",
			WithConstraintExtraction(false), WithMaxTurns(16), WithAutoCompaction(5000, 0)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return append([]CompactionInfo(nil), obs.seen...)
	}

	if control := run(0); len(control) != 0 {
		t.Fatalf("control compacted %d times; the history alone should be under the threshold", len(control))
	}
	seen := run(40)
	if len(seen) == 0 {
		t.Fatal("forty tool schemas over the threshold did not trigger compaction")
	}
	if seen[0].ContextTokens < 5000 {
		t.Fatalf("compaction measured %d tokens, below the threshold that triggered it", seen[0].ContextTokens)
	}
}

// A rewrite that keeps the count but changes the anchored prefix drops or
// shifts the anchor rather than trusting it.
func TestUsageAnchorFollowsClipping(t *testing.T) {
	tc := pool.NewTokenCounter()
	msgs := toolHeavyHistory(10, 5000)
	reported := tc.EstimateConversationTokens(msgs, "") + 4000
	svc, err := New("anchor-clip").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&stubSummaryLLM{}).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	r := &Runtime{svc: svc}
	r.anchor = newUsageAnchor(reported, msgs)

	clipped, n := clipOldToolResults(msgs, protectedHeadEnd(msgs), 4)
	if n == 0 {
		t.Fatal("nothing clipped")
	}
	r.shiftAnchorForClip(msgs, clipped)
	if !r.anchor.covers(clipped) {
		t.Fatal("anchor was dropped by a clip that kept the message count")
	}
	got, _ := anchoredTokens(tc, "", r.anchor, clipped)
	want := tc.EstimateConversationTokens(clipped, "") + 4000
	if diff := math.Abs(float64(got - want)); diff > float64(want)*0.02 {
		t.Fatalf("shifted anchor sizes the clipped history at %d, want about %d", got, want)
	}

	// A fold is a different history; the anchor must not cover it.
	folded := foldHistory(clipped, 1, len(clipped)-3, "summary", false)
	if r.anchor.covers(folded) {
		t.Fatal("an anchor covered a folded history it never saw")
	}
}
