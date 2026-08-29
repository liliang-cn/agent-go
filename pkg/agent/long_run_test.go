package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

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
