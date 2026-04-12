package agent

import (
	"context"
	"strings"
)

type RoutingSignal struct {
	Source string                   `json:"source"`
	Intent *IntentRecognitionResult `json:"intent,omitempty"`
}

type RoutingExplanation struct {
	Goal     string                   `json:"goal"`
	Signals  []RoutingSignal          `json:"signals"`
	Selected *IntentRecognitionResult `json:"selected,omitempty"`
}

func (s *Service) ExplainRouting(ctx context.Context, goal string, session *Session) (*RoutingExplanation, error) {
	if s == nil || s.planner == nil {
		return &RoutingExplanation{Goal: strings.TrimSpace(goal)}, nil
	}
	signals, err := s.planner.collectIntentSignals(ctx, goal, session)
	if err != nil {
		return nil, err
	}
	out := &RoutingExplanation{
		Goal:     strings.TrimSpace(goal),
		Signals:  make([]RoutingSignal, 0, len(signals)),
		Selected: s.planner.combineIntentSignals(goal, signals),
	}
	for _, signal := range signals {
		out.Signals = append(out.Signals, RoutingSignal{
			Source: signal.source,
			Intent: signal.intent,
		})
	}
	return out, nil
}
