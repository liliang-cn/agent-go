package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// scriptedLLM answers each *run* from a script: entry i drives the i-th
// segment. Within a segment it keeps asking for a tool until the segment's
// round budget runs out, unless the script says to finish.
type scriptedLLM struct {
	mu sync.Mutex
	// finishAt is the segment index (0-based) from which the model concludes
	// instead of looping. Before it, every turn asks for a tool.
	finishAt int
	// failAt segments return this error instead of answering.
	failAt map[int]error
	// segment counts how many runs have started, detected by the first turn
	// of a run carrying no assistant message yet.
	segment  int
	turns    int
	prompts  []string
	segments []int
}

func (l *scriptedLLM) begin(messages []domain.Message) (segment int, isFirstTurn bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := true
	for _, m := range messages {
		if m.Role == "assistant" || m.Role == "tool" {
			first = false
			break
		}
	}
	if first && l.turns > 0 {
		l.segment++
	}
	l.turns++
	l.segments = append(l.segments, l.segment)
	for _, m := range messages {
		if m.Role == "system" {
			l.prompts = append(l.prompts, m.Content)
			break
		}
	}
	return l.segment, first
}

func (l *scriptedLLM) snapshot() (segments []int, prompts []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.segments...), append([]string(nil), l.prompts...)
}

func (l *scriptedLLM) answer(messages []domain.Message, tools []domain.ToolDefinition) (*domain.GenerationResult, error) {
	seg, _ := l.begin(messages)
	l.mu.Lock()
	err := l.failAt[seg]
	finishAt := l.finishAt
	n := l.turns
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// The forced-synthesis pass runs with no tools; answer it with text.
	if len(tools) == 0 || seg >= finishAt {
		return &domain.GenerationResult{Content: "All done.", FinishReason: "stop"}, nil
	}
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   fmt.Sprintf("call-%d", n),
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "work",
				Arguments: map[string]interface{}{"n": float64(n)},
			},
		}},
		FinishReason: "tool_calls",
	}, nil
}

func (l *scriptedLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *scriptedLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *scriptedLLM) GenerateWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.answer(m, t)
}

func (l *scriptedLLM) StreamWithTools(_ context.Context, m []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := l.answer(m, t)
	if err != nil {
		return err
	}
	return cb(res)
}

func (l *scriptedLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *scriptedLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func buildSegmentedService(t *testing.T, name string, llm domain.Generator, store PlanStore) *Service {
	t.Helper()
	b := New(name).WithConfig(testAgentConfig(t.TempDir())).WithLLM(llm)
	if store != nil {
		b = b.WithPlanStore(store)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	svc.AddTool("work", "Does one unit of work.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		})
	return svc
}

// The core of it: a task that cannot finish inside one round budget is picked
// back up instead of being reported done with whatever it managed.
func TestRunSegmentsCarriesOnPastOneRoundBudget(t *testing.T) {
	llm := &scriptedLLM{finishAt: 2} // segments 0 and 1 loop; segment 2 concludes
	svc := buildSegmentedService(t, "segments-carry-on", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work through it.", LongRunConfig{
		MaxSegments:      5,
		RoundsPerSegment: 3,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if !res.Done() {
		t.Fatalf("stop = %q, want finished", res.Stop)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("ran %d segments, want 3", len(res.Segments))
	}
	if res.Segments[0].StopReason != StopReasonMaxTurns {
		t.Errorf("segment 0 stop = %q, want %q", res.Segments[0].StopReason, StopReasonMaxTurns)
	}
	if res.Segments[2].StopReason == StopReasonMaxTurns {
		t.Error("the final segment should have concluded, not run out of rounds")
	}
	if res.Text != "All done." {
		t.Errorf("text = %q", res.Text)
	}
}

// Each segment starts a fresh session, which is what stops the conversation
// growing across a task that runs for hours.
func TestRunSegmentsGivesEachSegmentAFreshSession(t *testing.T) {
	llm := &scriptedLLM{finishAt: 3}
	svc := buildSegmentedService(t, "segments-fresh-session", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:      3,
		RoundsPerSegment: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	seen := map[string]bool{}
	for _, seg := range res.Segments {
		if seg.SessionID == "" {
			t.Fatal("a segment ran with no session id")
		}
		if seen[seg.SessionID] {
			t.Errorf("segment %d reused session %s", seg.Index, seg.SessionID)
		}
		seen[seg.SessionID] = true
	}
	// One task across all of them, so checkpoints and the plan stay coherent.
	if res.TaskID == "" {
		t.Error("the long run had no task id")
	}
}

// Running out of segments is not an answer, and must not be reported as one.
func TestRunSegmentsReportsAnExhaustedBudgetHonestly(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99} // never concludes
	svc := buildSegmentedService(t, "segments-exhausted", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work forever.", LongRunConfig{
		MaxSegments:      2,
		RoundsPerSegment: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Done() {
		t.Fatal("an exhausted segment budget must not report the task as finished")
	}
	if res.Stop != LongRunStopSegmentBudget {
		t.Errorf("stop = %q, want %q", res.Stop, LongRunStopSegmentBudget)
	}
	if len(res.Segments) != 2 {
		t.Errorf("ran %d segments, want 2", len(res.Segments))
	}
}

// A failed segment is not the end of the task — the plan and the workspace
// survived it, so the next segment picks up where it died.
func TestRunSegmentsSurvivesAFailedSegment(t *testing.T) {
	llm := &scriptedLLM{
		finishAt: 2,
		failAt:   map[int]error{1: errors.New("401 Unauthorized: invalid api key")},
	}
	svc := buildSegmentedService(t, "segments-survive-failure", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:            5,
		RoundsPerSegment:       2,
		MaxConsecutiveFailures: 3,
		SegmentRetryBackoff:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if !res.Done() {
		t.Fatalf("stop = %q, want finished after recovering from one bad segment", res.Stop)
	}
	if len(res.Segments) < 3 {
		t.Fatalf("ran %d segments; the failed one should not have ended the task", len(res.Segments))
	}
	if res.Segments[1].Error == "" {
		t.Error("segment 1 was supposed to fail")
	}
}

// An outage that has swallowed several segments back to back is not going to
// be fixed by another one.
func TestRunSegmentsGivesUpOnConsecutiveFailures(t *testing.T) {
	llm := &scriptedLLM{
		finishAt: 99,
		failAt: map[int]error{
			0: errors.New("401 Unauthorized"),
			1: errors.New("401 Unauthorized"),
			2: errors.New("401 Unauthorized"),
			3: errors.New("401 Unauthorized"),
		},
	}
	svc := buildSegmentedService(t, "segments-give-up", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:            10,
		RoundsPerSegment:       2,
		MaxConsecutiveFailures: 2,
		SegmentRetryBackoff:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Stop != LongRunStopFailing {
		t.Errorf("stop = %q, want %q", res.Stop, LongRunStopFailing)
	}
	if len(res.Segments) != 2 {
		t.Errorf("ran %d segments, want to stop after 2 failures in a row", len(res.Segments))
	}
}

// A segment that says it is done while its own plan still has unchecked steps
// has not finished the task — it has stopped early, which is the failure mode
// a long run produces most.
func TestRunSegmentsKeepsGoingWhileThePlanHasUncheckedSteps(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0} // concludes immediately, every segment
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		scratchpadDefaultKey: {
			{Text: "step one", Done: true, Note: "did it"},
			{Text: "step two", Done: false},
		},
	}}
	svc := buildSegmentedService(t, "segments-plan-gate", llm, store)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:      3,
		RoundsPerSegment: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Done() {
		t.Fatal("a task with an unchecked plan step must not be reported as finished")
	}
	if len(res.Segments) != 3 {
		t.Errorf("ran %d segments, want all 3 — the plan never completed", len(res.Segments))
	}
	if !strings.Contains(res.PlanSummary, "step two") {
		t.Errorf("the result should carry the plan it did not finish, got: %q", res.PlanSummary)
	}
}

// The same run with the gate turned off finishes on the first segment, which
// is what makes the gate above a real check rather than a coincidence.
func TestAllowIncompletePlanEndsOnTheFirstConclusion(t *testing.T) {
	llm := &scriptedLLM{finishAt: 0}
	store := &memoryPlanStore{plans: map[string][]PlanItem{
		scratchpadDefaultKey: {
			{Text: "step one", Done: true, Note: "did it"},
			{Text: "step two", Done: false},
		},
	}}
	svc := buildSegmentedService(t, "segments-plan-gate-off", llm, store)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:         3,
		RoundsPerSegment:    2,
		AllowIncompletePlan: true,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if !res.Done() {
		t.Fatalf("stop = %q, want finished", res.Stop)
	}
	if len(res.Segments) != 1 {
		t.Errorf("ran %d segments, want 1", len(res.Segments))
	}
}

// blockedLLM concludes every segment with task_blocked.
type blockedLLM struct{}

func (l *blockedLLM) blocked() *domain.GenerationResult {
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   "block-1",
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "task_blocked",
				Arguments: map[string]interface{}{"blocker": "the door is locked and I have no key"},
			},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *blockedLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *blockedLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *blockedLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.blocked(), nil
}

func (l *blockedLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.blocked())
}

func (l *blockedLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *blockedLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// A considered "I cannot proceed" is an answer. Starting another segment would
// spend the budget arriving at it again.
func TestRunSegmentsStopsOnABlockedSegment(t *testing.T) {
	llm := &blockedLLM{}
	svc := buildSegmentedService(t, "segments-blocked", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Do the impossible.", LongRunConfig{
		MaxSegments:      4,
		RoundsPerSegment: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Stop != LongRunStopBlocked {
		t.Errorf("stop = %q, want %q", res.Stop, LongRunStopBlocked)
	}
	if len(res.Segments) != 1 {
		t.Errorf("ran %d segments, want to stop after the first block", len(res.Segments))
	}
}

// The caller's stop ends the task, and is not a failure.
func TestRunSegmentsHonoursCancellation(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99}
	svc := buildSegmentedService(t, "segments-cancel", llm, nil)
	defer svc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := svc.RunSegments(ctx, "Work.", LongRunConfig{MaxSegments: 5, RoundsPerSegment: 2})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Stop != LongRunStopCancelled {
		t.Errorf("stop = %q, want %q", res.Stop, LongRunStopCancelled)
	}
}

func TestLongRunConfigDefaults(t *testing.T) {
	t.Parallel()
	got := LongRunConfig{}.resolved()
	if got.MaxSegments != defaultMaxSegments {
		t.Errorf("MaxSegments = %d, want %d", got.MaxSegments, defaultMaxSegments)
	}
	if got.RoundsPerSegment != defaultRoundsPerSegment {
		t.Errorf("RoundsPerSegment = %d, want %d", got.RoundsPerSegment, defaultRoundsPerSegment)
	}
	if got.MaxConsecutiveFailures != defaultMaxConsecutiveFailures {
		t.Errorf("MaxConsecutiveFailures = %d, want %d", got.MaxConsecutiveFailures, defaultMaxConsecutiveFailures)
	}
	if got.PlanKey != scratchpadDefaultKey {
		t.Errorf("PlanKey = %q, want %q", got.PlanKey, scratchpadDefaultKey)
	}
}

// Budgets are checked between segments, so a task stops at a hand-off point
// with its plan and workspace consistent rather than being cut in half.
func TestRunSegmentsStopsOnTheTimeLimit(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99}
	svc := buildSegmentedService(t, "segments-time-limit", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work forever.", LongRunConfig{
		MaxSegments:      50,
		RoundsPerSegment: 2,
		// Long enough that the first segment starts, far too short for fifty.
		MaxDuration: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Stop != LongRunStopTimeLimit {
		t.Errorf("stop = %q, want %q", res.Stop, LongRunStopTimeLimit)
	}
	if res.Done() {
		t.Error("a task stopped by the clock has not finished")
	}
	// At least one, because the limit is checked between segments and never
	// inside one — a segment that started is allowed to finish. Nowhere near
	// fifty, because the limit did stop it.
	if n := len(res.Segments); n < 1 || n >= 50 {
		t.Errorf("ran %d segments, want at least 1 and well short of the 50 allowed", n)
	}
	for _, seg := range res.Segments {
		if seg.StopReason == "" && seg.Error == "" {
			t.Errorf("segment %d was cut off rather than allowed to finish", seg.Index)
		}
	}
}

// A per-run MaxBudgetUSD bounds one run, which on a task made of forty of them
// bounds nothing. This is the total.
func TestRunSegmentsStopsOnTheCostLimit(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99}
	svc := buildSegmentedService(t, "segments-cost-limit", llm, nil)
	defer svc.Close()
	svc.modelName = "gpt-4" // a model the pricing table knows, so cost is non-zero

	res, err := svc.RunSegments(context.Background(), "Work forever.", LongRunConfig{
		MaxSegments:      50,
		RoundsPerSegment: 2,
		MaxTotalCostUSD:  0.0000001,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Stop != LongRunStopCostLimit {
		t.Errorf("stop = %q, want %q (cost so far %f)", res.Stop, LongRunStopCostLimit, res.TotalCostUSD)
	}
	if res.TotalCostUSD <= 0 {
		t.Error("the task reported no cost at all; the per-segment figure is not reaching the total")
	}
}

// The wait after a failed segment is what makes MaxConsecutiveFailures mean
// anything: without it three failures are spent in seconds, against outages
// measured in tens of minutes.
func TestSegmentRetryDelayGrowsAndIsCapped(t *testing.T) {
	t.Parallel()
	base := 5 * time.Minute
	if got := segmentRetryDelay(base, 1); got != base {
		t.Errorf("first retry waits %s, want %s", got, base)
	}
	if got := segmentRetryDelay(base, 2); got != 2*base {
		t.Errorf("second retry waits %s, want %s", got, 2*base)
	}
	if got := segmentRetryDelay(base, 99); got != segmentRetryMaxBackoff {
		t.Errorf("a long outage waits %s, want the cap %s", got, segmentRetryMaxBackoff)
	}
	// Three consecutive failures should sit out enough time to be worth
	// calling patience — the whole point against a provider cooldown.
	total := segmentRetryDelay(base, 1) + segmentRetryDelay(base, 2) + segmentRetryDelay(base, 3)
	if total < 30*time.Minute {
		t.Errorf("three failures wait %s in total; a provider cooldown outlasts that", total)
	}
}

// The backoff must not turn a cancelled task into a half-hour sleep.
func TestRunSegmentsBackoffHonoursCancellation(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99, failAt: map[int]error{0: errors.New("401 Unauthorized")}}
	svc := buildSegmentedService(t, "segments-backoff-cancel", llm, nil)
	defer svc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	began := time.Now()
	res, err := svc.RunSegments(ctx, "Work.", LongRunConfig{
		MaxSegments:            5,
		RoundsPerSegment:       2,
		MaxConsecutiveFailures: 5,
		SegmentRetryBackoff:    time.Hour,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if elapsed := time.Since(began); elapsed > 30*time.Second {
		t.Fatalf("the cancelled task sat in the backoff for %s", elapsed)
	}
	if res.Stop != LongRunStopCancelled {
		t.Errorf("stop = %q, want %q", res.Stop, LongRunStopCancelled)
	}
}

func TestAddUsageKeepsNilWhenNothingWasReported(t *testing.T) {
	t.Parallel()
	if got := addUsage(nil, nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
	total := addUsage(nil, &domain.TokenUsage{PromptTokens: 10, CachedPromptTokens: 4})
	total = addUsage(total, &domain.TokenUsage{PromptTokens: 5, CacheWriteTokens: 2})
	total = addUsage(total, nil)
	if total.PromptTokens != 15 || total.CachedPromptTokens != 4 || total.CacheWriteTokens != 2 {
		t.Errorf("summed to %+v", total)
	}
}

// blockedOnBudgetLLM never concludes, so every segment exhausts its rounds and
// the forced synthesis is what the run ends on.
type blockedOnBudgetLLM struct{ toolLoopLLM }

// A segment that runs out of rounds is blocked with StopReasonMaxTurns when its
// forced synthesis fails a final lint. That is not a refusal — it is a segment
// that did not finish — and treating it as one ended a soak run at 9 of 13
// milestones while it was still making progress.
func TestBudgetExhaustionIsNotARefusal(t *testing.T) {
	t.Parallel()
	cfg := LongRunConfig{}.resolved()
	svc := &Service{}

	exhausted := &ExecutionResult{Success: false, Blocked: true, StopReason: StopReasonMaxTurns}
	if svc.segmentFinishedTheTask(exhausted, cfg) {
		t.Error("a segment that ran out of rounds has not finished the task")
	}

	// The supervisor's own branch: only a block that is *not* a budget
	// exhaustion ends the task.
	if exhausted.Blocked && exhausted.StopReason != StopReasonMaxTurns {
		t.Error("budget exhaustion must not be read as a considered refusal")
	}
	refusal := &ExecutionResult{Success: false, Blocked: true, StopReason: StopReasonLintExhausted}
	if !(refusal.Blocked && refusal.StopReason != StopReasonMaxTurns) {
		t.Error("a genuine block should still end the task")
	}
}

// End to end, with a lint that refuses everything so the forced synthesis at
// the end of each segment really does block. Before the fix the first segment
// ended the whole task; a soak run stopped that way at 9 of 13 milestones
// while it was still making progress.
type alwaysRejects struct{}

func (alwaysRejects) Name() string { return "always_rejects" }
func (alwaysRejects) Check(string, LintContext) (bool, string) {
	return false, "this lint refuses every answer"
}

func TestRunSegmentsKeepsGoingAfterABudgetBlock(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99}
	svc := buildSegmentedService(t, "segments-budget-block", llm, nil)
	defer svc.Close()
	svc.OutputLints().RegisterGlobal(alwaysRejects{})

	res, err := svc.RunSegments(context.Background(), "Work forever.", LongRunConfig{
		MaxSegments:      3,
		RoundsPerSegment: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	// Every segment here blocks on StopReasonMaxTurns. None of them refused
	// anything, so none of them should end the task.
	if res.Stop == LongRunStopBlocked {
		t.Fatalf("a segment that ran out of rounds ended the whole task as blocked "+
			"(segments run: %d)", len(res.Segments))
	}
	if len(res.Segments) != 3 {
		t.Errorf("ran %d segments, want all 3", len(res.Segments))
	}
	for _, seg := range res.Segments {
		if seg.StopReason != StopReasonMaxTurns {
			t.Errorf("segment %d stopped with %q, expected the budget to run out",
				seg.Index, seg.StopReason)
		}
	}
}

// readOnlyLLM asks for the same read every round and never writes anything —
// a segment too small to get past working out where it is.
type readOnlyLLM struct{ turns int32 }

func (l *readOnlyLLM) reply(tools []domain.ToolDefinition) *domain.GenerationResult {
	if len(tools) == 0 {
		return &domain.GenerationResult{Content: "Still orienting.", FinishReason: "stop"}
	}
	n := atomic.AddInt32(&l.turns, 1)
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID: fmt.Sprintf("r-%d", n), Type: "function",
			Function: domain.FunctionCall{
				Name:      "look",
				Arguments: map[string]interface{}{"n": float64(n)},
			},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *readOnlyLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (l *readOnlyLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (l *readOnlyLLM) GenerateWithTools(_ context.Context, _ []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(t), nil
}
func (l *readOnlyLLM) StreamWithTools(_ context.Context, _ []domain.Message, t []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(t))
}
func (l *readOnlyLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *readOnlyLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// Measured on a real run: at 15 rounds a segment spent everything it had
// re-reading the workspace and wrote not one byte, twice in a row, and the
// supervisor would have bought seventy-five more of them. A segment that
// changes nothing has not failed — it succeeded at doing nothing — so the
// failure budget never sees it.
func TestRunSegmentsStopsBuyingUnproductiveSegments(t *testing.T) {
	llm := &readOnlyLLM{}
	svc, err := New("segments-unproductive").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddToolWithMetadata("look", "Looks at something.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"n": map[string]interface{}{"type": "number"}}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "seen", nil },
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true})

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:             40,
		RoundsPerSegment:        2,
		MaxUnproductiveSegments: 3,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	if res.Stop != LongRunStopUnproductive {
		t.Fatalf("stop = %q, want %q after segments that changed nothing", res.Stop, LongRunStopUnproductive)
	}
	if len(res.Segments) != 3 {
		t.Errorf("bought %d segments, want to stop after 3 unproductive ones", len(res.Segments))
	}
	for _, seg := range res.Segments {
		if seg.Productive {
			t.Errorf("segment %d only read, and was counted as productive", seg.Index)
		}
	}
}

// A segment that writes resets the count, so an occasional orienting segment
// in a long task is not mistaken for a stall.
func TestOneQuietSegmentDoesNotEndTheTask(t *testing.T) {
	llm := &scriptedLLM{finishAt: 99}
	svc := buildSegmentedService(t, "segments-productive", llm, nil)
	defer svc.Close()

	res, err := svc.RunSegments(context.Background(), "Work.", LongRunConfig{
		MaxSegments:             4,
		RoundsPerSegment:        2,
		MaxUnproductiveSegments: 2,
	})
	if err != nil {
		t.Fatalf("RunSegments: %v", err)
	}
	// scriptedLLM calls a tool that is not declared read-only, so every
	// segment changes something and the count never builds.
	if res.Stop == LongRunStopUnproductive {
		t.Fatal("segments that called a state-changing tool were counted as unproductive")
	}
	for _, seg := range res.Segments {
		if !seg.Productive {
			t.Errorf("segment %d called a state-changing tool and was marked unproductive", seg.Index)
		}
	}
}
