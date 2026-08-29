package agent

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

type lintSpy struct {
	BaseObserver
	mu   sync.Mutex
	seen []LintInfo
}

func (s *lintSpy) OnLint(_ context.Context, info LintInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, info)
}

func (s *lintSpy) all() []LintInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]LintInfo(nil), s.seen...)
}

type plainTextLLM struct{}

func (plainTextLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}
func (plainTextLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}
func (plainTextLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return &domain.GenerationResult{Content: "Here is the answer.", FinishReason: "stop"}, nil
}
func (plainTextLLM) StreamWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(&domain.GenerationResult{Content: "Here is the answer.", FinishReason: "stop"})
}
func (plainTextLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}
func (plainTextLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// The lint layer is the one part of the runtime that ends a run on its own
// judgement, and its verdict was the one thing nothing recorded. A soak run
// finished all thirteen of its milestones, passed every test with the race
// detector, and was reported blocked — and which lint did it could not be
// answered from anything an observer could see.
func TestLintVerdictsReachObservers(t *testing.T) {
	spy := &lintSpy{}
	var logBuf bytes.Buffer

	svc, err := New("lint-observed").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(plainTextLLM{}).
		WithObserver(spy, NewActivityLog(&logBuf)).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.OutputLints().RegisterGlobal(alwaysRejects{})

	events, err := svc.RunStream(context.Background(), "Answer something.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	seen := spy.all()
	if len(seen) == 0 {
		t.Fatal("a lint rejected the answer and no observer heard about it")
	}
	for i, info := range seen {
		if info.Lint != "always_rejects" {
			t.Errorf("verdict %d names lint %q", i, info.Lint)
		}
		if info.Reason == "" {
			t.Errorf("verdict %d carries no reason", i)
		}
	}
	// The last one is the rejection the run is blocked on, not a retry.
	if last := seen[len(seen)-1]; last.Retrying {
		t.Error("the final rejection should be reported as the blocking one")
	}
	// And at least one retry came before it, or the budget was never spent.
	if len(seen) < 2 {
		t.Errorf("expected retries before the block, got %d verdicts", len(seen))
	}

	out := logBuf.String()
	if !strings.Contains(out, "always_rejects") || !strings.Contains(out, "BLOCKED by") {
		t.Errorf("the activity log should name the lint that ended the run:\n%s", out)
	}
}

// A run nothing rejects tells observers nothing.
func TestNoLintVerdictWhenNothingIsRejected(t *testing.T) {
	spy := &lintSpy{}
	svc, err := New("lint-quiet").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(plainTextLLM{}).
		WithObserver(spy).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Answer something.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}
	if n := len(spy.all()); n != 0 {
		t.Errorf("nothing was rejected but %d verdicts were reported", n)
	}
}

// The budget-exhaustion path blocks without going through lintGate, and that
// is precisely the path that produced tonight's unanswerable question: a run
// that ran out of rounds, had its forced answer rejected, and left no record
// of which lint did it. The first version of OnLint missed this case.
func TestLintVerdictIsReportedWhenTheBudgetRunsOut(t *testing.T) {
	spy := &lintSpy{}
	svc, err := New("lint-budget-exhausted").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&toolLoopLLM{}).
		WithObserver(spy).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	svc.AddTool("noop", "Does nothing.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		})
	svc.OutputLints().RegisterGlobal(alwaysRejects{})

	// The model never stops asking for tools, so the round budget is what ends
	// the run and the forced synthesis is what the lint rejects.
	events, err := svc.RunStreamWithOptions(context.Background(), "Work.", WithMaxTurns(2))
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}
	var stop StopReason
	for evt := range events {
		if evt.Type == EventTypeBlocked || evt.Type == EventTypeComplete {
			stop = evt.StopReason
		}
	}
	if stop != StopReasonMaxTurns {
		t.Fatalf("stop reason = %q, want %q — this test needs the budget path", stop, StopReasonMaxTurns)
	}
	seen := spy.all()
	if len(seen) == 0 {
		t.Fatal("the run was blocked by a lint after its budget ran out and no observer heard which one")
	}
	last := seen[len(seen)-1]
	if last.Lint != "always_rejects" {
		t.Errorf("verdict names %q", last.Lint)
	}
	if last.Retrying {
		t.Error("a verdict the run is blocked on must not be reported as a retry")
	}
}
