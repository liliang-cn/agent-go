// Package budgetgate is a third-party agent-go extension: a token ceiling
// across every run a service makes.
//
// It is the smallest useful extension that touches two seams. RunLifecycle
// refuses a run once the ceiling is reached; Observer adds up each model
// turn's tokens as it happens, so a run in flight is counted turn by turn
// rather than only when it ends. Nothing here is registered with the
// framework — the user lists it in WithExtensions and Build() finds the seams.
//
// The framework reports tokens and nothing else; a host that wants a money
// ceiling multiplies these counts by its own rates.
package budgetgate

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// Gate stops new runs once the service has used its token budget.
type Gate struct {
	agent.BaseObserver

	limit int

	mu      sync.Mutex
	used    int
	refused int
}

// New returns a gate with the given ceiling in tokens (prompt plus
// completion).
func New(limitTokens int) *Gate { return &Gate{limit: limitTokens} }

// Name implements agent.Extension.
func (g *Gate) Name() string { return "budget-gate" }

// OnRunStart implements agent.RunLifecycle. Returning an error blocks the run
// with that reason; the model is never called.
func (g *Gate) OnRunStart(_ context.Context, run agent.RunInfo) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used >= g.limit {
		g.refused++
		return fmt.Errorf("budget of %d tokens is used (%d so far); refusing %q", g.limit, g.used, run.Goal)
	}
	return nil
}

// OnRunEnd implements agent.RunLifecycle. The tokens were already added turn
// by turn; this is where a real gate would persist them.
func (g *Gate) OnRunEnd(context.Context, agent.RunInfo, agent.RunOutcome) {}

// OnModelEnd implements agent.Observer: count every turn as it completes.
func (g *Gate) OnModelEnd(_ context.Context, _ agent.ModelInfo, res *agent.ModelResult, _ error) {
	if res == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.used += res.PromptTokens + res.CompletionTokens
}

// Used reports the tokens counted so far.
func (g *Gate) Used() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

// Refused is how many runs the gate turned away.
func (g *Gate) Refused() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused
}

// Add records usage from outside — a previous process, a shared ledger.
func (g *Gate) Add(tokens int) {
	g.mu.Lock()
	g.used += tokens
	g.mu.Unlock()
}
