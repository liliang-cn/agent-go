package agent

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/decision"
)

// DefaultDecisionConfidence is the floor a gate answer must clear before the
// runtime acts on it. Below it the gate is treated as having said nothing and
// the full model call runs, so the floor only ever trades speed for certainty
// and never the other way.
const DefaultDecisionConfidence = 0.80

// decisionGateTimeout bounds one gate call. A gate exists to avoid a call that
// takes seconds; one that takes longer than this has stopped being worth
// asking, and the run proceeds exactly as it would with no engine at all.
const decisionGateTimeout = 2 * time.Second

// DecisionInfo is one gate consultation, reported whether or not it was acted
// on.
type DecisionInfo struct {
	// Gate names what was being decided, e.g. "run_constraints".
	Gate string
	// Engine is the engine's own name.
	Engine string
	// Label is the answer, in the engine's vocabulary.
	Label string
	// Confidence is how sure the engine was, in [0,1].
	Confidence float64
	// Floor is the confidence the answer had to clear.
	Floor float64
	// Acted reports whether the runtime used the answer. False means the
	// engine was unsure, failed or was too slow, and the ordinary path ran.
	Acted bool
	// Skipped reports whether acting on it avoided a model call. This is the
	// number that says whether the engine is earning its place.
	Skipped bool
	// Duration is how long the consultation took.
	Duration time.Duration
	// Err is why the gate could not be used, when it could not.
	Err error
}

// DecisionObserver receives every gate consultation.
//
// Optional, for the reason ResourceObserver is: Observer is implemented
// outside this repository and must not grow a method.
//
//	type myObs struct{ agent.BaseObserver }
//	func (myObs) OnDecision(_ context.Context, d agent.DecisionInfo) { … }
type DecisionObserver interface {
	OnDecision(ctx context.Context, info DecisionInfo)
}

// emitDecision hands one consultation to whoever asked for them.
func (s *Service) emitDecision(ctx context.Context, info DecisionInfo) {
	if s == nil {
		return
	}
	s.observersMu.RLock()
	observers := make([]Observer, len(s.observers))
	copy(observers, s.observers)
	s.observersMu.RUnlock()
	for _, o := range observers {
		if d, ok := o.(DecisionObserver); ok {
			d.OnDecision(ctx, info)
		}
	}
}

// decisionFloor is the configured confidence floor, or the default.
func (s *Service) decisionFloor() float64 {
	if s == nil || s.decisionConfidence <= 0 {
		return DefaultDecisionConfidence
	}
	return s.decisionConfidence
}

// The gate asks one question per category RunConstraints can hold, never one
// compound question covering all three.
//
// That is not a style preference; it was measured. A single noul naming all
// three categories at once got 9 of 10 constrained goals wrong, several of
// them confidently — "给张伟发一封邮件" came back as "asks for nothing" at 0.99.
// Asked as its own question the same model, on the same text, answers "an
// email" at 1.00. A compound question has no single answer, so the engine
// returns one anyway and its confidence means nothing.
//
// Asking three costs nothing: the engine answers every question it is given in
// one forward pass, so the whole gate is still one request and one pass.
//
// The categories are named, not exemplified in one language. The engine reads
// the user's own words in whatever language they wrote them; there is no
// phrase table here and there must never be one.
var constraintGateQuestions = map[string]decision.Question{
	"produce": decision.Choice(
		"What concrete artifact does the user ask the assistant to produce or send?",
		"a file", "an email", "a chat message", "a document", "nothing"),
	"act": decision.Noul(
		"Does the user ask the assistant to set a reminder, add a calendar entry, or record a note?"),
	"forbid": decision.Noul(
		"Does the user tell the assistant not to use tools?"),
}

func (s *Service) goalNeedsNoConstraints(ctx context.Context, goal string) bool {
	if s == nil || s.decisionEngine == nil || strings.TrimSpace(goal) == "" {
		return false
	}

	floor := s.decisionFloor()
	info := DecisionInfo{
		Gate:   "run_constraints",
		Engine: s.decisionEngine.Name(),
		Floor:  floor,
	}

	gateCtx, cancel := context.WithTimeout(ctx, decisionGateTimeout)
	defer cancel()

	started := time.Now()
	answers, err := s.decisionEngine.Decide(gateCtx, goal, constraintGateQuestions)
	info.Duration = time.Since(started)

	if err != nil {
		info.Err = err
		s.emitDecision(ctx, info)
		// A decision engine is an optimisation. It going away is not an
		// incident, and must not read like one in a log somebody greps for
		// real failures.
		s.logger.Debug("decision gate unavailable; extracting constraints as usual",
			slog.String("engine", info.Engine),
			slog.String("error", err.Error()))
		return false
	}

	clear, confidence, weakest := readConstraintGate(answers)
	info.Label = weakest
	info.Confidence = confidence
	info.Acted = confidence >= floor
	info.Skipped = clear && info.Acted
	s.emitDecision(ctx, info)

	return info.Skipped
}

// readConstraintGate folds the answers into one verdict.
//
// The gate's confidence is the *least* confident of the answers, because the
// skip needs all of them to be right. Taking the average, or the answer that
// happened to be surest, would let one category nobody was sure about ride in
// on the certainty of the other two.
func readConstraintGate(answers map[string]decision.Answer) (clear bool, confidence float64, weakest string) {
	produce, act, forbid := answers["produce"], answers["act"], answers["forbid"]
	clear = produce.Label == "nothing" && !act.Yes && !forbid.Yes

	confidence, weakest = 2.0, ""
	for _, a := range []struct {
		name string
		decision.Answer
	}{{"produce", produce}, {"act", act}, {"forbid", forbid}} {
		if a.Label == "" {
			// An answer that never arrived cannot vouch for anything.
			return false, 0, a.name + "=?"
		}
		if a.Confidence < confidence {
			confidence, weakest = a.Confidence, a.name+"="+a.Label
		}
	}
	return clear, confidence, weakest
}
