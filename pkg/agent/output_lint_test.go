package agent

import (
	"strings"
	"testing"
)

func passLint(name string) OutputLint {
	return LintFunc{
		NameValue: name,
		Fn: func(text string, ctx LintContext) (bool, string) {
			return true, ""
		},
	}
}

func failLint(name, reason string) OutputLint {
	return LintFunc{
		NameValue: name,
		Fn: func(text string, ctx LintContext) (bool, string) {
			return false, reason
		},
	}
}

func TestOutputLintRegistryGlobalRunsForEveryAgent(t *testing.T) {
	reg := NewOutputLintRegistry()
	reg.RegisterGlobal(failLint("global_block", "blocked globally"))

	got := reg.Run("anything", LintContext{AgentName: "Operator"})
	if got == nil || got.LintName != "global_block" {
		t.Fatalf("expected global lint to fire, got %+v", got)
	}

	got = reg.Run("anything", LintContext{AgentName: "Responder"})
	if got == nil || got.LintName != "global_block" {
		t.Fatalf("expected global lint to fire for second agent, got %+v", got)
	}
}

func TestOutputLintRegistryAgentSpecificIsolation(t *testing.T) {
	reg := NewOutputLintRegistry()
	reg.RegisterForAgent("Dispatcher", failLint("dispatcher_only", "only dispatcher"))

	if got := reg.Run("anything", LintContext{AgentName: "Operator"}); got != nil {
		t.Fatalf("expected dispatcher-only lint not to fire for Operator, got %+v", got)
	}

	got := reg.Run("anything", LintContext{AgentName: "Dispatcher"})
	if got == nil || got.LintName != "dispatcher_only" {
		t.Fatalf("expected dispatcher lint to fire, got %+v", got)
	}
}

func TestOutputLintRegistryMatchesAgentCaseInsensitively(t *testing.T) {
	reg := NewOutputLintRegistry()
	reg.RegisterForAgent("Archivist", failLint("archivist_lint", "x"))

	for _, name := range []string{"Archivist", "archivist", "ARCHIVIST", "  Archivist  "} {
		got := reg.Run("anything", LintContext{AgentName: name})
		if got == nil {
			t.Fatalf("expected match for agent %q", name)
		}
	}
}

func TestOutputLintRegistryGlobalRunsBeforeAgentSpecific(t *testing.T) {
	reg := NewOutputLintRegistry()
	reg.RegisterGlobal(failLint("global_first", "global"))
	reg.RegisterForAgent("Operator", failLint("operator_specific", "operator"))

	got := reg.Run("anything", LintContext{AgentName: "Operator"})
	if got == nil || got.LintName != "global_first" {
		t.Fatalf("expected global lint to win, got %+v", got)
	}
}

func TestOutputLintRegistryStopsAtFirstViolation(t *testing.T) {
	reg := NewOutputLintRegistry()
	reg.RegisterGlobal(passLint("global_pass"))
	reg.RegisterForAgent("Operator", failLint("first_fail", "first"))
	reg.RegisterForAgent("Operator", failLint("second_fail", "second"))

	got := reg.Run("anything", LintContext{AgentName: "Operator"})
	if got == nil || got.LintName != "first_fail" {
		t.Fatalf("expected first failing lint, got %+v", got)
	}
}

func TestOutputLintRegistryNilSafe(t *testing.T) {
	var reg *OutputLintRegistry
	if got := reg.Run("anything", LintContext{}); got != nil {
		t.Fatalf("nil registry should never violate, got %+v", got)
	}
	// Should not panic.
	reg.RegisterGlobal(failLint("noop", "noop"))
	reg.RegisterForAgent("X", failLint("noop", "noop"))
	if names := reg.Names("X"); names != nil {
		t.Fatalf("nil registry Names should be nil, got %v", names)
	}
}

func TestOutputLintRegistryNamesOrdering(t *testing.T) {
	reg := NewOutputLintRegistry()
	reg.RegisterGlobal(passLint("g1"))
	reg.RegisterGlobal(passLint("g2"))
	reg.RegisterForAgent("Operator", passLint("op1"))
	reg.RegisterForAgent("Operator", passLint("op2"))

	names := reg.Names("Operator")
	want := []string{"g1", "g2", "op1", "op2"}
	if len(names) != len(want) {
		t.Fatalf("name count mismatch: want %v got %v", want, names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("at %d: want %q got %q (full=%v)", i, want[i], n, names)
		}
	}
}

func TestFormatLintFeedbackContainsLintNameAndReason(t *testing.T) {
	v := &LintViolation{LintName: "dispatcher_no_bounce_back", Reason: "response routes the task back to Dispatcher"}
	feedback := FormatLintFeedback(v)
	if !strings.Contains(feedback, "dispatcher_no_bounce_back") {
		t.Fatalf("feedback missing lint name: %q", feedback)
	}
	if !strings.Contains(feedback, "routes the task back to Dispatcher") {
		t.Fatalf("feedback missing reason: %q", feedback)
	}
	if !strings.Contains(feedback, "revised final answer") {
		t.Fatalf("feedback missing retry instruction: %q", feedback)
	}
}

func TestFormatLintFeedbackWithEmptyViolation(t *testing.T) {
	if got := FormatLintFeedback(nil); got != "" {
		t.Fatalf("nil violation should yield empty feedback, got %q", got)
	}
}

func TestLintFuncNilFnIsPass(t *testing.T) {
	lint := LintFunc{NameValue: "noop"}
	ok, reason := lint.Check("anything", LintContext{})
	if !ok || reason != "" {
		t.Fatalf("nil-fn lint should pass with no reason, got ok=%v reason=%q", ok, reason)
	}
}
