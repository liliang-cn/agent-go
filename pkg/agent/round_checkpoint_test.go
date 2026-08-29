package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// recordingSink captures everything the runtime asks to persist.
type recordingCheckpointSink struct {
	mu     sync.Mutex
	writes []checkpointWrite
}

// errRunDeadForTest is a permanent failure: not retried, so the run ends on it.
var errRunDeadForTest = errors.New("401 Unauthorized: invalid api key")

type checkpointWrite struct {
	Reason    CheckpointReason
	Round     int
	Messages  int
	Workspace bool
}

func (s *recordingCheckpointSink) WriteCheckpoint(_ string, reason CheckpointReason, round int, _, _, _, _ string, messages []domain.Message, workspace []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, checkpointWrite{
		Reason:    reason,
		Round:     round,
		Messages:  len(messages),
		Workspace: workspace != nil,
	})
	return nil
}

func (s *recordingCheckpointSink) byReason(reason CheckpointReason) []checkpointWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []checkpointWrite
	for _, w := range s.writes {
		if w.Reason == reason {
			out = append(out, w)
		}
	}
	return out
}

// toolLoopLLM asks for one tool call per round until the round budget runs
// out, so the run has several completed rounds to snapshot.
type toolLoopLLM struct{ turns int32 }

func (l *toolLoopLLM) reply(tools []domain.ToolDefinition) *domain.GenerationResult {
	if len(tools) == 0 {
		return &domain.GenerationResult{Content: "Out of rounds.", FinishReason: "stop"}
	}
	n := atomic.AddInt32(&l.turns, 1)
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   fmt.Sprintf("call-%d", n),
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "noop",
				Arguments: map[string]interface{}{"n": float64(n)},
			},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *toolLoopLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *toolLoopLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *toolLoopLLM) GenerateWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(tools), nil
}

func (l *toolLoopLLM) StreamWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	return cb(l.reply(tools))
}

func (l *toolLoopLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *toolLoopLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func buildCheckpointingService(t *testing.T, name string, autonomy AutonomyProfile) (*Service, *recordingCheckpointSink) {
	t.Helper()
	svc, err := New(name).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&toolLoopLLM{}).
		WithAutonomy(autonomy).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sink := &recordingCheckpointSink{}
	svc.SetCheckpointSink(sink)
	svc.AddTool("noop", "Does nothing.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		})
	return svc, sink
}

// A run in flight has to exist on disk. Before this, the only three writers
// were complete, blocked and cancelled — so the run worth resuming, the one
// still going, was the one never written down.
func TestInFlightRunWritesRoundCheckpoints(t *testing.T) {
	svc, sink := buildCheckpointingService(t, "round-checkpoints", AutonomyProfile{MaxRounds: 4})
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Keep going.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	rounds := sink.byReason(CheckpointReasonRoundEnd)
	if len(rounds) != 4 {
		t.Fatalf("wrote %d round checkpoints, want one per round (4)", len(rounds))
	}
	for i, w := range rounds {
		if w.Round != i+1 {
			t.Errorf("checkpoint %d is for round %d, want %d", i, w.Round, i+1)
		}
		if w.Messages == 0 {
			t.Errorf("checkpoint for round %d snapshotted no messages", w.Round)
		}
		// Tarring the sandbox every round would cost more than the loop.
		if w.Workspace {
			t.Errorf("round checkpoint %d archived the workspace", w.Round)
		}
	}
}

// The interval is a real knob, not a comment.
func TestCheckpointEveryRoundsThrottlesWrites(t *testing.T) {
	svc, sink := buildCheckpointingService(t, "round-checkpoints-throttled",
		AutonomyProfile{MaxRounds: 6, CheckpointEveryRounds: 3})
	defer svc.Close()

	events, err := svc.RunStream(context.Background(), "Keep going.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range events {
	}

	rounds := sink.byReason(CheckpointReasonRoundEnd)
	if len(rounds) != 2 {
		t.Fatalf("wrote %d round checkpoints over 6 rounds at interval 3, want 2", len(rounds))
	}
	if rounds[0].Round != 3 || rounds[1].Round != 6 {
		t.Errorf("snapshotted rounds %d and %d, want 3 and 6", rounds[0].Round, rounds[1].Round)
	}
}

// deadAfterLLM answers the first round with a tool call and then fails
// permanently, standing in for a gateway that dies mid-run.
type deadAfterLLM struct {
	mu     sync.Mutex
	rounds int
	err    error
}

func (l *deadAfterLLM) next(tools []domain.ToolDefinition) (*domain.GenerationResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rounds <= 0 {
		return nil, l.err
	}
	l.rounds--
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:       fmt.Sprintf("call-%d", l.rounds),
			Type:     "function",
			Function: domain.FunctionCall{Name: "noop", Arguments: map[string]interface{}{}},
		}},
		FinishReason: "tool_calls",
	}, nil
}

func (l *deadAfterLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *deadAfterLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *deadAfterLLM) GenerateWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.next(tools)
}

func (l *deadAfterLLM) StreamWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	res, err := l.next(tools)
	if err != nil {
		return err
	}
	return cb(res)
}

func (l *deadAfterLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *deadAfterLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// The nineteenth hour. A run that dies to an unrecoverable provider error has
// to leave the work behind it on disk — the old code returned straight out of
// the loop and wrote nothing at all.
func TestFailedRunLeavesAResumableCheckpoint(t *testing.T) {
	svc, err := New("failed-run-checkpoint").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&deadAfterLLM{rounds: 2, err: errRunDeadForTest}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()
	sink := &recordingCheckpointSink{}
	svc.SetCheckpointSink(sink)
	svc.AddTool("noop", "Does nothing.",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		})

	events, err := svc.RunStream(context.Background(), "Work until the gateway dies.")
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var sawError bool
	var stop StopReason
	for evt := range events {
		if evt.Type == EventTypeError {
			sawError = true
			stop = evt.StopReason
		}
	}
	if !sawError {
		t.Fatal("expected a workflow_error event")
	}
	if stop != StopReasonErrorDuringExecution {
		t.Errorf("stop reason = %q, want %q", stop, StopReasonErrorDuringExecution)
	}

	failed := sink.byReason(CheckpointReasonRunFailed)
	if len(failed) != 1 {
		t.Fatalf("wrote %d run_failed checkpoints, want 1 — a failed run must be resumable", len(failed))
	}
	if failed[0].Messages == 0 {
		t.Error("the failure checkpoint snapshotted no messages; there is nothing to resume from")
	}
	// The two rounds that did succeed should be on disk too.
	if rounds := sink.byReason(CheckpointReasonRoundEnd); len(rounds) != 2 {
		t.Errorf("wrote %d round checkpoints, want 2", len(rounds))
	}
}
