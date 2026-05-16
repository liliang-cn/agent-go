package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestLeadingSystemCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string // roles only
		want int
	}{
		{"none", []string{"user", "assistant"}, 0},
		{"one", []string{"system", "user", "assistant"}, 1},
		{"two stacked", []string{"system", "system", "user"}, 2},
		{"empty", nil, 0},
	}
	for _, tc := range cases {
		msgs := rolesToMessages(tc.in)
		got := leadingSystemCount(msgs)
		if got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestPickTailStart_AvoidsOrphanedToolRole(t *testing.T) {
	t.Parallel()
	// History: [sys, user, assistant(tool_call), tool, assistant, user, assistant]
	// keepRecent=3 would naively start at index 4 ("assistant"), but if we
	// shifted the slice such that index 4 is a "tool" role, we'd orphan
	// its preceding tool_call. Test the role-walkback directly.
	msgs := rolesToMessages([]string{
		"system", "user", "assistant", "tool", "tool", "assistant",
	})
	// Pretend keepRecent=3 starts at index 3 ("tool"). pickTailStart
	// must walk back until a non-tool role.
	start := pickTailStart(msgs, leadingSystemCount(msgs), 3)
	if start == len(msgs)-3 {
		t.Fatalf("tail start %d points at a tool message; should have walked back", start)
	}
	if msgs[start].Role == "tool" {
		t.Fatalf("tail start lands on tool role: %+v", msgs[start])
	}
}

// stubSummaryLLM is a Generator that returns a fixed summary string and
// records how many times Generate was called. Other LLM methods return
// zero values; the compactor only uses Generate.
type stubSummaryLLM struct {
	summary string
	calls   int32
}

func (s *stubSummaryLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.summary, nil
}
func (s *stubSummaryLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	return nil
}
func (s *stubSummaryLLM) GenerateWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{}, nil
}
func (s *stubSummaryLLM) StreamWithTools(ctx context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{})
}
func (s *stubSummaryLLM) GenerateStructured(ctx context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (s *stubSummaryLLM) RecognizeIntent(ctx context.Context, _ string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestCompactMessages_PreservesHeadAndTail(t *testing.T) {
	t.Parallel()
	svc, err := New("compaction-test").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&stubSummaryLLM{summary: "Summary of earlier rounds: user asked X; tool Y returned Z."}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	msgs := []domain.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "old turn 1"},
		{Role: "assistant", Content: "old reply 1"},
		{Role: "user", Content: "old turn 2"},
		{Role: "assistant", Content: "old reply 2"},
		{Role: "user", Content: "recent 1"},
		{Role: "assistant", Content: "recent 1 reply"},
		{Role: "user", Content: "recent 2"},
	}

	out, err := svc.compactMessages(context.Background(), msgs, 3)
	if err != nil {
		t.Fatalf("compactMessages: %v", err)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("compaction did not shrink history: in=%d out=%d", len(msgs), len(out))
	}

	// Head preserved verbatim
	if out[0].Role != "system" || out[0].Content != "You are a helpful assistant." {
		t.Errorf("system head not preserved: %+v", out[0])
	}

	// Second message is the summary
	if out[1].Role != "system" || !strings.Contains(out[1].Content, "COMPACTED CONVERSATION SUMMARY") {
		t.Errorf("expected summary as second message, got: %+v", out[1])
	}
	if !strings.Contains(out[1].Content, "tool Y returned Z") {
		t.Errorf("summary content missing: %s", out[1].Content)
	}

	// Tail (last 3) preserved verbatim
	tail := out[len(out)-3:]
	if tail[0].Content != "recent 1" || tail[1].Content != "recent 1 reply" || tail[2].Content != "recent 2" {
		t.Errorf("tail not preserved verbatim: %+v", tail)
	}
}

func TestCompactMessages_NoOpForShortHistory(t *testing.T) {
	t.Parallel()
	svc, err := New("compaction-noop-test").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&stubSummaryLLM{summary: "should not be used"}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	msgs := []domain.Message{
		{Role: "system", Content: "S"},
		{Role: "user", Content: "U"},
		{Role: "assistant", Content: "A"},
	}
	out, err := svc.compactMessages(context.Background(), msgs, 6)
	if err != nil {
		t.Fatalf("compactMessages: %v", err)
	}
	if len(out) != len(msgs) {
		t.Errorf("expected no-op when keepRecent covers entire body, got len %d", len(out))
	}
}

// scriptedCompactRuntimeLLM exposes both Generate (for the summarizer)
// and StreamWithTools (for the runtime loop). It runs the summarizer
// in-thread and threads a sequence of tool-loop replies back.
type scriptedCompactRuntimeLLM struct {
	mu              sync.Mutex
	loopReplies     []*domain.GenerationResult
	loopCalls       int32
	summarizerCalls int32
	summary         string
	capturedAfter   int // first round captured by StreamWithTools after compaction
	capturedSizes   []int
}

func (l *scriptedCompactRuntimeLLM) Generate(ctx context.Context, _ string, _ *domain.GenerationOptions) (string, error) {
	atomic.AddInt32(&l.summarizerCalls, 1)
	return l.summary, nil
}
func (l *scriptedCompactRuntimeLLM) Stream(ctx context.Context, _ string, _ *domain.GenerationOptions, _ func(string)) error {
	return nil
}
func (l *scriptedCompactRuntimeLLM) GenerateWithTools(ctx context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.nextLoop(msgs), nil
}
func (l *scriptedCompactRuntimeLLM) StreamWithTools(ctx context.Context, msgs []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.nextLoop(msgs))
}
func (l *scriptedCompactRuntimeLLM) GenerateStructured(ctx context.Context, _ string, _ interface{}, _ *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (l *scriptedCompactRuntimeLLM) RecognizeIntent(ctx context.Context, _ string) (*domain.IntentResult, error) {
	return nil, nil
}
func (l *scriptedCompactRuntimeLLM) nextLoop(msgs []domain.Message) *domain.GenerationResult {
	idx := int(atomic.AddInt32(&l.loopCalls, 1)) - 1
	l.mu.Lock()
	l.capturedSizes = append(l.capturedSizes, len(msgs))
	l.mu.Unlock()
	if idx >= len(l.loopReplies) {
		idx = len(l.loopReplies) - 1
	}
	if idx < 0 {
		return &domain.GenerationResult{}
	}
	r := *l.loopReplies[idx]
	r.ToolCalls = append([]domain.ToolCall(nil), l.loopReplies[idx].ToolCalls...)
	return &r
}

// TestRuntime_AutoCompaction_FullFlow drives a streaming run where:
//   - round 1 calls a tool (adds assistant + tool_result messages)
//   - round 2 emits text that the lint rejects, forcing a retry
//   - round 3 hits the top-of-loop auto-compaction (message count is now
//     large enough that the compactor produces a meaningful summary)
//   - round 3 produces a clean final answer
//
// Verifies the summarizer fires, compact_boundary event is emitted, the
// PreCompact hook receives the pre-compaction messages, and the post-
// compaction LLM call sees a strictly smaller message slice.
func TestRuntime_AutoCompaction_FullFlow(t *testing.T) {
	llm := &scriptedCompactRuntimeLLM{
		loopReplies: []*domain.GenerationResult{
			// Round 1: call a tool. This appends assistant + tool_result.
			{
				Content: "",
				ToolCalls: []domain.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: domain.FunctionCall{
							Name:      "echo_tool",
							Arguments: map[string]interface{}{"msg": "hi"},
						},
					},
				},
			},
			// Round 2: text response that fails the lint.
			{Content: "REJECT_ME " + strings.Repeat("verbose padding ", 200)},
			// Round 3: clean final answer (after compaction + retry).
			{Content: "Final answer."},
		},
		summary: "Earlier the model called echo_tool with msg=hi; user wants a concise final answer.",
	}

	svc, err := New("auto-compaction-runtime").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(llm).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	// Register a tiny tool so round 1's tool_call has somewhere to land.
	svc.Register(BuildTool("echo_tool").
		Description("Echo input").
		Param("msg", TypeString, "message", Required()).
		Handler(func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return fmt.Sprintf("echoed:%v", args["msg"]), nil
		}).
		Build())

	// Reject round 2 once so the loop continues into round 3 — that's
	// where the top-of-loop compaction check has enough history to fire.
	svc.RegisterOutputLint(&rejectIfContains{
		name:    "reject_padding",
		needle:  "REJECT_ME",
		reason:  "padding rejected; reply concisely.",
		maxFail: 1,
	})

	var preCompactFired int32
	var preCompactBefore int
	svc.RegisterHook(HookEventPreCompact, func(ctx context.Context, _ HookEvent, data HookData) (interface{}, error) {
		atomic.AddInt32(&preCompactFired, 1)
		preCompactBefore = len(data.MessagesBefore)
		return data, nil
	})

	events, err := svc.RunStreamWithOptions(context.Background(),
		"Generate a long answer about X.",
		WithAutoCompaction(50, 2), // tiny threshold so the lint-retry round trips compaction
	)
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}

	var sawCompactBoundary bool
	var compactBefore, compactAfter int
	var final, blocked string
	for evt := range events {
		switch evt.Type {
		case EventTypeCompactBoundary:
			sawCompactBoundary = true
		case EventTypeAnalytics:
			if evt.AnalyticsEvent != nil && evt.AnalyticsEvent.Name == AnalyticsAutocompactTriggered {
				if v, ok := evt.AnalyticsEvent.Data["messages_before"].(int); ok {
					compactBefore = v
				}
				if v, ok := evt.AnalyticsEvent.Data["messages_after"].(int); ok {
					compactAfter = v
				}
			}
		case EventTypeComplete:
			final = evt.Content
		case EventTypeBlocked:
			blocked = evt.Content
		}
	}

	if blocked != "" {
		t.Fatalf("expected completion, got blocked=%q", blocked)
	}
	if !sawCompactBoundary {
		t.Fatal("expected a compact_boundary event")
	}
	if atomic.LoadInt32(&llm.summarizerCalls) == 0 {
		t.Fatal("summarizer was never called")
	}
	if atomic.LoadInt32(&preCompactFired) == 0 {
		t.Fatal("PreCompact hook never fired")
	}
	if preCompactBefore == 0 {
		t.Fatal("PreCompact hook received empty MessagesBefore")
	}
	if final != "Final answer." {
		t.Fatalf("final = %q, want %q", final, "Final answer.")
	}

	if compactBefore == 0 || compactAfter == 0 {
		t.Fatalf("autocompact analytics event not seen: before=%d after=%d", compactBefore, compactAfter)
	}
	if compactAfter >= compactBefore {
		t.Fatalf("compaction did not shrink: before=%d after=%d", compactBefore, compactAfter)
	}
}

// rolesToMessages is a test helper for building skeleton messages from a
// role list.
func rolesToMessages(roles []string) []domain.Message {
	out := make([]domain.Message, 0, len(roles))
	for _, r := range roles {
		out = append(out, domain.Message{Role: r, Content: r + "-content"})
	}
	return out
}
