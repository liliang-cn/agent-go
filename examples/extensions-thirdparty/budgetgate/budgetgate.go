// Package budgetgate is a third-party agent-go extension: a spending ceiling
// across every run a service makes.
//
// It is the smallest useful extension that touches two seams. RunLifecycle
// refuses a run once the ceiling is reached and adds up what each run cost
// when it ends; Observer prices each model turn as it happens, so a run in
// flight cannot overshoot by a whole run. Nothing here is registered with the
// framework — the user lists it in WithExtensions and Build() finds the seams.
package budgetgate

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// Gate stops new runs once the service has spent its budget.
type Gate struct {
	agent.BaseObserver

	limit float64

	mu       sync.Mutex
	spent    float64
	unpriced int
	refused  int
}

// New returns a gate with the given ceiling in USD.
func New(limitUSD float64) *Gate { return &Gate{limit: limitUSD} }

// Name implements agent.Extension.
func (g *Gate) Name() string { return "budget-gate" }

// OnRunStart implements agent.RunLifecycle. Returning an error blocks the run
// with that reason; the model is never called.
func (g *Gate) OnRunStart(_ context.Context, run agent.RunInfo) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.spent >= g.limit {
		g.refused++
		return fmt.Errorf("budget of $%.2f is spent ($%.4f so far); refusing %q", g.limit, g.spent, run.Goal)
	}
	return nil
}

// OnRunEnd implements agent.RunLifecycle. The spend was already added turn by
// turn; this is where a real gate would persist it.
func (g *Gate) OnRunEnd(context.Context, agent.RunInfo, agent.RunOutcome) {}

// OnModelEnd implements agent.Observer: price every turn as it completes.
func (g *Gate) OnModelEnd(_ context.Context, info agent.ModelInfo, res *agent.ModelResult, _ error) {
	if res == nil {
		return
	}
	cost, priced := pool.CalculateCostDetailed(info.Model, res.PromptTokens, res.CachedTokens, res.CompletionTokens)
	g.mu.Lock()
	defer g.mu.Unlock()
	if priced {
		g.spent += cost
	} else {
		g.unpriced++
	}
}

// Spent reports the priced spend so far and how many turns could not be
// priced — those are unknown, not free, and a cautious caller treats a
// non-zero count as a reason to register the model's price.
func (g *Gate) Spent() (usd float64, unpricedTurns int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.spent, g.unpriced
}

// Refused is how many runs the gate turned away.
func (g *Gate) Refused() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused
}

// Add records spend from outside — a previous process, a shared ledger.
func (g *Gate) Add(usd float64) {
	g.mu.Lock()
	g.spent += usd
	g.mu.Unlock()
}
