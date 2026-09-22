package agent

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/decision"
)

// ModelRoute is one destination a run can be sent to.
type ModelRoute struct {
	// Name is a short handle for logs and observers.
	Name string
	// Model and Provider name a client the pool was configured with. A route
	// naming something the pool does not serve is a preference it ignores,
	// which is why RouteInfo reports the model that answered.
	Model    string
	Provider string
	// Description says what kind of work belongs here, in plain words. This
	// is the only thing a router reads: it is the question, not a label.
	//
	// Write what the work looks like, not what the model is. "A direct
	// lookup or a short factual answer" routes; "Haiku, it's cheaper" does
	// not, because the thing choosing has never heard of your bill.
	Description string
}

// ModelRouter picks the model for a run before its first turn.
//
// Returning false means "no opinion", and the run proceeds on the pool's own
// choice. A router is consulted only when the caller did not name a model
// itself: an explicit WithModel is a decision already made.
type ModelRouter interface {
	Route(ctx context.Context, goal string) (ModelRoute, bool)
}

// ConfidentModelRouter is a ModelRouter that also reports how sure it was.
// Optional: the runtime prefers it when a router implements it, so the
// confidence in RouteInfo belongs to that call and not to a field two
// concurrent runs are both writing.
type ConfidentModelRouter interface {
	ModelRouter
	RouteWithConfidence(ctx context.Context, goal string) (ModelRoute, float64, bool)
}

// RouteInfo is one routing decision, reported whether or not it changed
// anything.
type RouteInfo struct {
	// Route is what the router picked. Zero when it had no opinion.
	Route ModelRoute
	// Routed reports whether a route was applied to the run.
	Routed bool
	// Explicit reports that the caller named the model itself, so no router
	// ran.
	Explicit bool
	// Confidence is the router's own, when it has one.
	Confidence float64
	// Duration is how long the decision took.
	Duration time.Duration
	// Err is why routing could not happen, when it could not.
	Err error
}

// RouteObserver receives every routing decision.
//
// Optional, like ResourceObserver and DecisionObserver, and for the same
// reason: Observer is implemented outside this repository.
type RouteObserver interface {
	OnRoute(ctx context.Context, info RouteInfo)
}

func (s *Service) emitRoute(ctx context.Context, info RouteInfo) {
	if s == nil {
		return
	}
	s.observersMu.RLock()
	observers := make([]Observer, len(s.observers))
	copy(observers, s.observers)
	s.observersMu.RUnlock()
	for _, o := range observers {
		if r, ok := o.(RouteObserver); ok {
			r.OnRoute(ctx, info)
		}
	}
}

// routeRun settles which model this run should use, once, before its first
// turn.
//
// Precedence, highest first: the caller's own WithModel, the router, the
// pool's strategy. An explicit choice is never second-guessed — the same rule
// resolveRunConstraints follows for declared constraints, and for the same
// reason: a caller who said what they wanted has already paid the thinking
// cost.
func (s *Service) routeRun(ctx context.Context, goal string, cfg *RunConfig) {
	if s == nil || cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Model) != "" || strings.TrimSpace(cfg.Provider) != "" {
		s.setRunModel(cfg.RunID, cfg.Model, cfg.Provider)
		s.emitRoute(ctx, RouteInfo{
			Explicit: true,
			Route:    ModelRoute{Model: cfg.Model, Provider: cfg.Provider},
		})
		return
	}
	if s.modelRouter == nil || strings.TrimSpace(goal) == "" {
		return
	}

	started := time.Now()
	var (
		route      ModelRoute
		ok         bool
		confidence float64
	)
	// A router that knows how sure it is reports that per call. The plain
	// interface stays two values so a third party can implement it with a
	// switch statement and no notion of confidence at all.
	if c, reports := s.modelRouter.(ConfidentModelRouter); reports {
		route, confidence, ok = c.RouteWithConfidence(ctx, goal)
	} else {
		route, ok = s.modelRouter.Route(ctx, goal)
	}
	info := RouteInfo{Route: route, Duration: time.Since(started), Confidence: confidence}

	if ok && (route.Model != "" || route.Provider != "") {
		cfg.Model, cfg.Provider = route.Model, route.Provider
		info.Routed = true
		s.setRunModel(cfg.RunID, cfg.Model, cfg.Provider)
	}
	s.emitRoute(ctx, info)
}

// decisionRouter routes with a decision engine: one closed question over the
// routes' own descriptions.
//
// It asks a choice, and it must keep asking a choice. Measured on laya-mlx
// over ten goals split evenly between a lookup and a hard problem:
//
//	choice over the descriptions   8/10 right, mean confidence 0.48
//	choice over short labels       7/10 right, mean confidence 0.51
//	noul "does this need           5/10 right, mean confidence 0.94
//	  extended reasoning?"
//
// The noul form is confident and no better than a coin toss, which is the
// worst thing a gated decision can be: the floor stops protecting anything,
// and hard problems go to the cheap model with a 0.94 beside them. The choice
// form is accurate and under-confident, so the floor turns its uncertainty
// into "leave it to the pool".
//
// The cost of that is how rarely it fires — on that corpus, one or two goals
// in eight clear 0.80 and the rest are left alone. A router that declines is
// working as intended; one that is sure and wrong is not. Raise the rate by
// writing descriptions that contrast sharply, and re-run
// TestDecisionRouterAgainstLiveEngine rather than trusting that they read well.
type decisionRouter struct {
	engine decision.Engine
	routes []ModelRoute
	floor  float64
	logger *slog.Logger
}

// NewDecisionRouter routes with a decision engine. Routes need distinct
// descriptions — the description is what the engine chooses between, so two
// routes described the same way are one route.
//
// confidence is the floor an answer must clear; 0 takes
// DefaultDecisionConfidence. Below it the run is left to the pool, which is
// the same degradation every other use of an engine here takes: unsure means
// carry on as if there were no engine.
func NewDecisionRouter(engine decision.Engine, confidence float64, routes ...ModelRoute) ModelRouter {
	if confidence <= 0 {
		confidence = DefaultDecisionConfidence
	}
	return &decisionRouter{
		engine: engine,
		routes: routes,
		floor:  confidence,
		logger: slog.Default(),
	}
}

// Route satisfies ModelRouter; RouteWithConfidence is what the runtime
// actually calls.
func (d *decisionRouter) Route(ctx context.Context, goal string) (ModelRoute, bool) {
	route, _, ok := d.RouteWithConfidence(ctx, goal)
	return route, ok
}

func (d *decisionRouter) RouteWithConfidence(ctx context.Context, goal string) (ModelRoute, float64, bool) {
	if d == nil || d.engine == nil || len(d.routes) < 2 {
		// One route is not a choice, and routing to the only destination
		// there is would cost a call to learn nothing.
		return ModelRoute{}, 0, false
	}

	criteria := make([]string, 0, len(d.routes))
	byDescription := make(map[string]ModelRoute, len(d.routes))
	for _, r := range d.routes {
		desc := strings.TrimSpace(r.Description)
		if desc == "" {
			continue
		}
		if _, clash := byDescription[desc]; clash {
			continue
		}
		criteria = append(criteria, desc)
		byDescription[desc] = r
	}
	if len(criteria) < 2 {
		return ModelRoute{}, 0, false
	}

	routeCtx, cancel := context.WithTimeout(ctx, decisionGateTimeout)
	defer cancel()

	answers, err := d.engine.Decide(routeCtx, goal, map[string]decision.Question{
		"route": decision.Choice("Which of these best describes the work this request asks for?", criteria...),
	})
	if err != nil {
		d.logger.Debug("model router unavailable; leaving the model to the pool",
			slog.String("error", err.Error()))
		return ModelRoute{}, 0, false
	}

	answer := answers["route"]
	if answer.Confidence < d.floor {
		// Unsure routes nowhere. The pool's own strategy is a defensible
		// default; a coin flip between a cheap model and an expensive one is
		// not.
		return ModelRoute{}, answer.Confidence, false
	}
	route, ok := byDescription[answer.Label]
	return route, answer.Confidence, ok
}
