package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// roundCountingLLM asks for one tool call every turn and never produces a
// final answer, so the only way the loop can end is by running out of rounds.
// Each call carries a fresh call id and fresh arguments so neither the
// runtime's call-id dedupe nor its repeat detection can collapse two rounds
// into one — the tool execution count is then exactly the round count.
type roundCountingLLM struct {
	turns int32
}

func (l *roundCountingLLM) reply() *domain.GenerationResult {
	n := atomic.AddInt32(&l.turns, 1)
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   fmt.Sprintf("call-%d", n),
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "count_round",
				Arguments: map[string]interface{}{"n": float64(n)},
			},
		}},
		FinishReason: "tool_calls",
	}
}

func (l *roundCountingLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *roundCountingLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *roundCountingLLM) GenerateWithTools(context.Context, []domain.Message, []domain.ToolDefinition, *domain.GenerationOptions) (*domain.GenerationResult, error) {
	return l.reply(), nil
}

func (l *roundCountingLLM) StreamWithTools(_ context.Context, _ []domain.Message, tools []domain.ToolDefinition, _ *domain.GenerationOptions, cb domain.ToolCallCallback) error {
	// The forced final-synthesis pass runs with the tool list emptied. Asking
	// for a tool there would be refused and would not count as a round, so
	// answer it with plain text instead.
	if len(tools) == 0 {
		return cb(&domain.GenerationResult{Content: "Out of rounds.", FinishReason: "stop"})
	}
	return cb(l.reply())
}

func (l *roundCountingLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{Valid: true, Raw: "{}"}, nil
}

func (l *roundCountingLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

// runCountingRounds builds a service whose model loops forever on one tool,
// runs it, and reports how many times that tool actually executed plus the
// stop reason the run terminated with.
func runCountingRounds(t *testing.T, name string, build func(*Builder) *Builder, opts ...RunOption) (int, StopReason) {
	t.Helper()

	b := New(name).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&roundCountingLLM{})
	if build != nil {
		b = build(b)
	}
	svc, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	var executed int32
	svc.AddTool("count_round", "Counts one round.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"n": map[string]interface{}{"type": "number"}},
		},
		func(context.Context, map[string]interface{}) (interface{}, error) {
			atomic.AddInt32(&executed, 1)
			return map[string]interface{}{"ok": true}, nil
		})

	events, err := svc.RunStreamWithOptions(context.Background(), "Keep going.", opts...)
	if err != nil {
		t.Fatalf("RunStreamWithOptions: %v", err)
	}
	var stop StopReason
	for evt := range events {
		switch evt.Type {
		case EventTypeComplete, EventTypeBlocked:
			stop = evt.StopReason
		}
	}
	return int(atomic.LoadInt32(&executed)), stop
}

// TestRunHonoursWithMaxTurns is the per-run half of the budget: a run that
// asks for three rounds gets three, not the framework's twenty.
func TestRunHonoursWithMaxTurns(t *testing.T) {
	rounds, stop := runCountingRounds(t, "max-turns-run", nil, WithMaxTurns(3))
	if rounds != 3 {
		t.Errorf("ran %d rounds, want 3 (WithMaxTurns is not reaching the loop)", rounds)
	}
	if stop != StopReasonMaxTurns {
		t.Errorf("stop reason = %q, want %q", stop, StopReasonMaxTurns)
	}
}

// TestRunHonoursAutonomyMaxRounds is the per-service half: a long-horizon
// agent sets its budget once, and a run that does not override it inherits it.
func TestRunHonoursAutonomyMaxRounds(t *testing.T) {
	rounds, stop := runCountingRounds(t, "max-turns-autonomy", func(b *Builder) *Builder {
		return b.WithAutonomy(AutonomyProfile{MaxRounds: 4})
	})
	if rounds != 4 {
		t.Errorf("ran %d rounds, want 4 (WithAutonomy is not reaching the loop)", rounds)
	}
	if stop != StopReasonMaxTurns {
		t.Errorf("stop reason = %q, want %q", stop, StopReasonMaxTurns)
	}
}

// TestRunMaxTurnsBeatsAutonomy pins the precedence: the run is more specific
// than the service, so its budget wins.
func TestRunMaxTurnsBeatsAutonomy(t *testing.T) {
	rounds, _ := runCountingRounds(t, "max-turns-precedence", func(b *Builder) *Builder {
		return b.WithAutonomy(AutonomyProfile{MaxRounds: 9})
	}, WithMaxTurns(2))
	if rounds != 2 {
		t.Errorf("ran %d rounds, want 2 (the run's budget must beat the service's)", rounds)
	}
}

func TestResolveMaxRounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		run      int
		autonomy int
		want     int
	}{
		{"neither set falls back to the framework default", 0, 0, DefaultMaxRounds},
		{"the run's budget is used when set", 250, 0, 250},
		{"the service's budget is used when the run has none", 0, 300, 300},
		{"the run beats the service", 7, 300, 7},
		{"a non-positive run budget means unset, not zero rounds", -1, 300, 300},
		{"a non-positive service budget means unset too", 0, -1, DefaultMaxRounds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runtime{
				cfg: &RunConfig{MaxTurns: tc.run},
				svc: &Service{defaultMaxTurns: tc.autonomy},
			}
			if got := r.resolveMaxRounds(); got != tc.want {
				t.Errorf("resolveMaxRounds() = %d, want %d", got, tc.want)
			}
		})
	}
}
