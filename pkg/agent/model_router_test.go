package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/decision"
)

var testRoutes = []ModelRoute{
	{Name: "fast", Model: "cheap-model", Description: "A direct lookup or a short factual answer"},
	{Name: "deep", Model: "smart-model", Description: "Design, debugging, or a decision with consequences"},
}

func choiceAnswer(label string, confidence float64) map[string]decision.Answer {
	return map[string]decision.Answer{
		"route": {Type: decision.TypeChoice, Label: label, Confidence: confidence},
	}
}

func TestDecisionRouterPicksTheDescribedRoute(t *testing.T) {
	engine := &stubEngine{answers: choiceAnswer(testRoutes[1].Description, 0.95)}
	route, confidence, ok := NewDecisionRouter(engine, 0, testRoutes...).(ConfidentModelRouter).
		RouteWithConfidence(context.Background(), "redesign the storage layer")
	if !ok {
		t.Fatal("a confident answer routed nowhere")
	}
	if route.Model != "smart-model" {
		t.Errorf("routed to %s, want smart-model", route.Model)
	}
	if confidence != 0.95 {
		t.Errorf("confidence = %v, want 0.95", confidence)
	}
}

func TestDecisionRouterDeclinesRatherThanGuesses(t *testing.T) {
	// Unsure must route nowhere. A coin flip between a cheap model and an
	// expensive one is worse than the pool's own default, which at least is
	// the same every time.
	cases := map[string]*stubEngine{
		"below the floor":        {answers: choiceAnswer(testRoutes[0].Description, 0.31)},
		"engine unreachable":     {err: errors.New("connection refused")},
		"an answer for no route": {answers: choiceAnswer("something nobody described", 0.99)},
	}
	for name, engine := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := NewDecisionRouter(engine, 0, testRoutes...).Route(context.Background(), "do a thing"); ok {
				t.Error("routed anyway")
			}
		})
	}
}

func TestDecisionRouterNeedsAChoiceToMake(t *testing.T) {
	// One route, or two routes described identically, is not a choice — and
	// asking costs a call to learn nothing.
	for name, routes := range map[string][]ModelRoute{
		"a single route":         testRoutes[:1],
		"no descriptions":        {{Name: "a", Model: "x"}, {Name: "b", Model: "y"}},
		"identical descriptions": {{Name: "a", Model: "x", Description: "same"}, {Name: "b", Model: "y", Description: "same"}},
	} {
		t.Run(name, func(t *testing.T) {
			engine := &stubEngine{answers: choiceAnswer("same", 1)}
			if _, ok := NewDecisionRouter(engine, 0, routes...).Route(context.Background(), "do a thing"); ok {
				t.Error("routed without a choice to make")
			}
			if engine.calls != 0 {
				t.Errorf("asked the engine %d times with nothing to choose between", engine.calls)
			}
		})
	}
}

// fixedRouter always picks the same place.
type fixedRouter struct {
	route ModelRoute
	calls int
}

func (f *fixedRouter) Route(context.Context, string) (ModelRoute, bool) {
	f.calls++
	return f.route, true
}

func TestAnExplicitModelBeatsTheRouter(t *testing.T) {
	// A caller who named a model has already made the decision. Re-deciding
	// it would make WithModel a suggestion, which is not what it reads like.
	router := &fixedRouter{route: ModelRoute{Model: "router-model"}}
	svc := &Service{modelRouter: router, logger: slog.Default()}
	cfg := DefaultRunConfig()
	cfg.Model = "caller-model"

	svc.routeRun(context.Background(), "do a thing", cfg)

	if cfg.Model != "caller-model" {
		t.Errorf("model = %s, want the caller's", cfg.Model)
	}
	if router.calls != 0 {
		t.Error("the router ran even though the caller had chosen")
	}
}

func TestRoutingIsReportedEvenWhenItChangesNothing(t *testing.T) {
	var seen []RouteInfo
	svc := &Service{
		modelRouter: NewDecisionRouter(&stubEngine{answers: choiceAnswer(testRoutes[0].Description, 0.20)}, 0, testRoutes...),
		logger:      slog.Default(),
	}
	svc.observers = []Observer{&routeRecorder{onto: &seen}}

	cfg := DefaultRunConfig()
	svc.routeRun(context.Background(), "what is a mutex?", cfg)

	if len(seen) != 1 {
		t.Fatalf("observed %d routing decisions, want 1", len(seen))
	}
	if seen[0].Routed {
		t.Error("reported as routed despite being under the floor")
	}
	if seen[0].Confidence != 0.20 {
		t.Errorf("confidence = %v, want the 0.20 it was unsure at", seen[0].Confidence)
	}
	if cfg.Model != "" {
		t.Errorf("model = %q, want it left to the pool", cfg.Model)
	}
}

func TestNoRouterLeavesEverythingAlone(t *testing.T) {
	svc := &Service{logger: slog.Default()}
	cfg := DefaultRunConfig()
	svc.routeRun(context.Background(), "do a thing", cfg)
	if cfg.Model != "" || cfg.Provider != "" {
		t.Errorf("a service with no router set model %q provider %q", cfg.Model, cfg.Provider)
	}
}

type routeRecorder struct {
	BaseObserver
	onto *[]RouteInfo
}

func (r *routeRecorder) OnRoute(_ context.Context, info RouteInfo) { *r.onto = append(*r.onto, info) }
