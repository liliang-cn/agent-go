package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/decision"
)

// stubEngine replays a fixed set of answers.
type stubEngine struct {
	answers map[string]decision.Answer
	err     error
	delay   time.Duration
	calls   int
}

func (s *stubEngine) Name() string { return "stub" }

func (s *stubEngine) Decide(ctx context.Context, _ string, qs map[string]decision.Question) (map[string]decision.Answer, error) {
	s.calls++
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]decision.Answer, len(qs))
	for name := range qs {
		if a, ok := s.answers[name]; ok {
			out[name] = a
		}
	}
	return out, nil
}

// clearGate is the answer set for a goal that asks for nothing, at the
// confidence given.
func clearGate(confidence float64) map[string]decision.Answer {
	return map[string]decision.Answer{
		"produce": {Type: decision.TypeChoice, Label: "nothing", Confidence: confidence},
		"act":     noulAnswer(false, 1),
		"forbid":  noulAnswer(false, 1),
	}
}

func noulAnswer(yes bool, confidence float64) decision.Answer {
	label := "no"
	if yes {
		label = "yes"
	}
	return decision.Answer{Type: decision.TypeNoul, Yes: yes, Label: label, Confidence: confidence}
}

func TestDecisionGateSkipsOnlyWhenEveryCategoryIsAConfidentNo(t *testing.T) {
	cases := []struct {
		name     string
		answers  map[string]decision.Answer
		wantSkip bool
	}{
		{"nothing asked for, all confident", clearGate(0.99), true},
		{"exactly at the floor clears it", clearGate(DefaultDecisionConfidence), true},
		{"unsure about the artifact", clearGate(0.42), false},
		// Each category alone is enough to require the extraction: the engine
		// cannot produce the constraint it just said exists, because filling
		// one needs the user's own words and a tool name from this catalog.
		{"an artifact was asked for", map[string]decision.Answer{
			"produce": {Type: decision.TypeChoice, Label: "an email", Confidence: 1},
			"act":     noulAnswer(false, 1),
			"forbid":  noulAnswer(false, 1),
		}, false},
		{"an action was asked for", map[string]decision.Answer{
			"produce": {Type: decision.TypeChoice, Label: "nothing", Confidence: 1},
			"act":     noulAnswer(true, 1),
			"forbid":  noulAnswer(false, 1),
		}, false},
		{"tools were forbidden", map[string]decision.Answer{
			"produce": {Type: decision.TypeChoice, Label: "nothing", Confidence: 1},
			"act":     noulAnswer(false, 1),
			"forbid":  noulAnswer(true, 1),
		}, false},
		// The weakest answer decides. Two certainties must not carry a third
		// nobody was sure about.
		{"one shaky category sinks two certain ones", map[string]decision.Answer{
			"produce": {Type: decision.TypeChoice, Label: "nothing", Confidence: 1},
			"act":     noulAnswer(false, 0.31),
			"forbid":  noulAnswer(false, 1),
		}, false},
		// A reply missing a category vouches for nothing.
		{"a category went unanswered", map[string]decision.Answer{
			"produce": {Type: decision.TypeChoice, Label: "nothing", Confidence: 1},
			"forbid":  noulAnswer(false, 1),
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &stubEngine{answers: tc.answers}
			svc := &Service{decisionEngine: engine, logger: slog.Default()}
			if got := svc.goalNeedsNoConstraints(context.Background(), "do the thing"); got != tc.wantSkip {
				t.Errorf("skip = %v, want %v", got, tc.wantSkip)
			}
			if engine.calls != 1 {
				t.Errorf("engine consulted %d times, want once", engine.calls)
			}
		})
	}
}

func TestDecisionGateDegradesRatherThanBlocks(t *testing.T) {
	// Every one of these is a reason the gate cannot answer. None of them may
	// stop a run: the gate is an optimisation, and an optimisation that can
	// break a run is a liability.
	t.Run("engine error", func(t *testing.T) {
		svc := &Service{decisionEngine: &stubEngine{err: errors.New("connection refused")}, logger: slog.Default()}
		if svc.goalNeedsNoConstraints(context.Background(), "do the thing") {
			t.Error("a failed gate skipped the extraction")
		}
	})

	t.Run("engine too slow", func(t *testing.T) {
		svc := &Service{decisionEngine: &stubEngine{delay: 50 * time.Millisecond, answers: clearGate(1)}, logger: slog.Default()}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		if svc.goalNeedsNoConstraints(ctx, "do the thing") {
			t.Error("a timed-out gate skipped the extraction")
		}
	})

	t.Run("no engine configured", func(t *testing.T) {
		svc := &Service{logger: slog.Default()}
		if svc.goalNeedsNoConstraints(context.Background(), "do the thing") {
			t.Error("a service with no engine skipped the extraction")
		}
	})

	t.Run("empty goal", func(t *testing.T) {
		engine := &stubEngine{answers: clearGate(1)}
		svc := &Service{decisionEngine: engine, logger: slog.Default()}
		if svc.goalNeedsNoConstraints(context.Background(), "   ") {
			t.Error("an empty goal skipped the extraction")
		}
		if engine.calls != 0 {
			t.Error("an empty goal was still sent to the engine")
		}
	})
}

func TestDecisionGateReportsEveryConsultation(t *testing.T) {
	var got []DecisionInfo
	svc := &Service{
		decisionEngine:     &stubEngine{answers: clearGate(0.5)},
		decisionConfidence: 0.9,
		logger:             slog.Default(),
	}
	svc.observers = []Observer{&decisionRecorder{onto: &got}}

	svc.goalNeedsNoConstraints(context.Background(), "do the thing")

	if len(got) != 1 {
		t.Fatalf("observed %d consultations, want 1", len(got))
	}
	info := got[0]
	// An unsure answer is still reported: a gate that never clears its floor
	// is pure added latency, and only the report makes that visible.
	if info.Acted || info.Skipped {
		t.Errorf("acted=%v skipped=%v, want both false below the floor", info.Acted, info.Skipped)
	}
	if info.Floor != 0.9 {
		t.Errorf("Floor = %v, want the configured 0.9", info.Floor)
	}
	if info.Gate != "run_constraints" || info.Engine != "stub" {
		t.Errorf("Gate/Engine = %q/%q", info.Gate, info.Engine)
	}
}

func TestDecisionFloorFallsBackToTheDefault(t *testing.T) {
	if got := (&Service{}).decisionFloor(); got != DefaultDecisionConfidence {
		t.Errorf("unset floor = %v, want %v", got, DefaultDecisionConfidence)
	}
	if got := (&Service{decisionConfidence: 0.55}).decisionFloor(); got != 0.55 {
		t.Errorf("configured floor = %v, want 0.55", got)
	}
}

type decisionRecorder struct {
	BaseObserver
	onto *[]DecisionInfo
}

func (r *decisionRecorder) OnDecision(_ context.Context, info DecisionInfo) {
	*r.onto = append(*r.onto, info)
}
