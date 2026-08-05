package agent

import (
	"context"
	"sync"
)

// discoveryVerdict is the outcome of asking the run's budget whether a tool
// search may execute.
type discoveryVerdict int

const (
	// discoveryAllowed: a new search, within budget. Run it.
	discoveryAllowed discoveryVerdict = iota
	// discoveryRepeat: this query was already searched in this run. Searching
	// the same catalog twice cannot return anything new.
	discoveryRepeat
	// discoveryExhausted: budget spent on other queries. Stop discovering.
	discoveryExhausted
)

// discoveryBudget bounds distinct tool searches for one run.
//
// It lives in the run context rather than in the execution loop's state
// because tool searches reach the registry from two directions: chat-protocol
// tool calls, and `callTool('search_available_tools', ...)` written by the
// model inside the PTC JavaScript sandbox. The sandbox calls the registered
// handler directly (pkg/ptc/runtime/goja/runtime.go), so a budget enforced in
// the chat loop alone just pushes the search loop inside execute_javascript.
//
// Safe for concurrent use: chat-path tools execute in parallel goroutines.
type discoveryBudget struct {
	mu      sync.Mutex
	limit   int
	queries map[string]struct{}
}

func newDiscoveryBudget(limit int) *discoveryBudget {
	if limit <= 0 {
		limit = maxToolDiscoveryCallsPerRun
	}
	return &discoveryBudget{limit: limit, queries: make(map[string]struct{}, limit)}
}

// admit records a query and reports whether the search may run. A repeat does
// not consume budget — otherwise one query looped in the sandbox would spend
// the whole allowance.
func (b *discoveryBudget) admit(query string) discoveryVerdict {
	if b == nil {
		return discoveryAllowed
	}
	key := normalizeQueryText(query)

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, seen := b.queries[key]; seen {
		return discoveryRepeat
	}
	if len(b.queries) >= b.limit {
		return discoveryExhausted
	}
	b.queries[key] = struct{}{}
	return discoveryAllowed
}

// discoveryBudgetKey carries the per-run tool-discovery budget. The runtime
// installs it at loop start, next to the tool-use sink.
const discoveryBudgetKey contextKey = "tool_discovery_budget"

func withDiscoveryBudget(ctx context.Context, b *discoveryBudget) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, discoveryBudgetKey, b)
}

// ensureDiscoveryBudget installs a run-scoped budget unless the caller already
// supplied one. Nesting (a subagent, or Service.Run reached from inside a
// runtime loop) then shares the outer allowance instead of quietly resetting
// it, which would hand the model a fresh set of searches per nesting level.
func ensureDiscoveryBudget(ctx context.Context) context.Context {
	if discoveryBudgetFromContext(ctx) != nil {
		return ctx
	}
	return withDiscoveryBudget(ctx, newDiscoveryBudget(maxToolDiscoveryCallsPerRun))
}

func discoveryBudgetFromContext(ctx context.Context) *discoveryBudget {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(discoveryBudgetKey).(*discoveryBudget)
	return b
}

// admitToolDiscovery is the guard every tool-search entry point calls. It
// returns replacement guidance and true when the search must not run.
//
// With no budget in context (a Service used directly as a library object,
// outside a runtime loop) discovery stays unbounded — this must not change
// behavior for embedders.
func admitToolDiscovery(ctx context.Context, query string) (string, bool) {
	switch discoveryBudgetFromContext(ctx).admit(query) {
	case discoveryExhausted:
		return toolDiscoveryBudgetGuidance, true
	case discoveryRepeat:
		return toolDiscoveryRepeatGuidance, true
	default:
		return "", false
	}
}
