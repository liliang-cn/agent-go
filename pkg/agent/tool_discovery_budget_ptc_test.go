package agent

import (
	"context"
	"strings"
	"testing"
)

func TestDiscoveryBudgetAllowsDistinctQueriesThenExhausts(t *testing.T) {
	t.Parallel()

	b := newDiscoveryBudget(3)

	for _, q := range []string{"send email", "smtp", "mail"} {
		if v := b.admit(q); v != discoveryAllowed {
			t.Fatalf("query %q: verdict = %v, want discoveryAllowed", q, v)
		}
	}

	// Re-asking something already searched is a repeat, not a new search: it
	// must not burn a slot, or a sandbox loop would exhaust the budget with
	// one query.
	if v := b.admit("smtp"); v != discoveryRepeat {
		t.Fatalf("repeat query verdict = %v, want discoveryRepeat", v)
	}

	if v := b.admit("outbound mail"); v != discoveryExhausted {
		t.Fatalf("over-budget query verdict = %v, want discoveryExhausted", v)
	}
}

// The budget added in handleDuplicateToolCalls only covers chat-protocol tool
// calls. PTC runs model-written JavaScript that calls `callTool(...)` straight
// into the registered tool handler (pkg/ptc/runtime/goja/runtime.go), which
// never passes through that check — so the search loop simply moves inside
// execute_javascript.
//
// ToolRegistry.Call with a run-scoped context is exactly what the sandbox
// does, so this covers the PTC path.
func TestToolSearchHandlerRefusesPastDiscoveryBudget(t *testing.T) {
	t.Parallel()

	svc, err := New("ptc-discovery-budget").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLintLLM{replies: []string{""}}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	ctx := withDiscoveryBudget(context.Background(), newDiscoveryBudget(3))

	queries := []string{"send email", "email", "mail sender", "smtp client", "outbound mail"}
	refused := 0
	for _, q := range queries {
		res, err := svc.toolRegistry.Call(ctx, "search_available_tools", map[string]interface{}{"query": q})
		if err != nil {
			t.Fatalf("query %q: unexpected error: %v", q, err)
		}
		if text, ok := res.(string); ok && strings.Contains(text, "task_blocked") {
			refused++
		}
	}

	if refused != 2 {
		t.Fatalf("expected the last 2 of 5 searches to be refused with guidance, got %d", refused)
	}
}

// A Service used as a plain library object (agent.New(...).Build(), no runtime
// loop) has no budget installed. Discovery must keep working exactly as before
// rather than silently refusing.
func TestToolSearchWithoutBudgetInContextIsUnbounded(t *testing.T) {
	t.Parallel()

	svc, err := New("no-discovery-budget").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLintLLM{replies: []string{""}}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	for _, q := range []string{"a", "b", "c", "d", "e", "f"} {
		res, err := svc.toolRegistry.Call(context.Background(), "search_available_tools", map[string]interface{}{"query": q})
		if err != nil {
			t.Fatalf("query %q: unexpected error: %v", q, err)
		}
		if text, ok := res.(string); ok && strings.Contains(text, "task_blocked") {
			t.Fatalf("query %q was refused even though no budget is installed", q)
		}
	}
}

// The default budget is what ships, so it is what actually matters. Measured
// on agentbench (30 tasks, paired, same judge): a budget of 3 never binds —
// 13 tasks up, 13 down, p=1.00, pure noise. A budget of 1 cuts total tool
// calls 28% (17 down, 5 up, p=0.017) with task completion unchanged.
//
// So: one search. If it finds nothing usable, answer or block.
func TestDefaultDiscoveryBudgetAllowsOneSearch(t *testing.T) {
	t.Parallel()

	b := newDiscoveryBudget(maxToolDiscoveryCallsPerRun)

	if v := b.admit("send email"); v != discoveryAllowed {
		t.Fatalf("first search verdict = %v, want discoveryAllowed", v)
	}
	if v := b.admit("mail sender"); v != discoveryExhausted {
		t.Fatalf("second distinct search verdict = %v, want discoveryExhausted", v)
	}
}
