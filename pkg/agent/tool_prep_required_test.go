package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A tool the run is contractually required to call must reach the schema even
// when a narrowing policy would have dropped it. Otherwise the delivery
// contract waits for a call the model has no way to make, and the model —
// reading its own schema — tells the user the capability does not exist. That
// is the shape of the worst benchmark failure this pair of changes removes.
func TestRequiredToolSurvivesANarrowedSchema(t *testing.T) {
	t.Parallel()

	svc, err := New("required-tools").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&constraintLLM{replies: []string{"ok"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	svc.AddTool("set_reminder", "Create a reminder",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(context.Context, map[string]interface{}) (interface{}, error) { return "ok", nil })

	cfg := DefaultRunConfig()
	cfg.resolvedConstraints = &RunConstraints{
		RequestedActions: []RequestedAction{{Kind: "reminder", SatisfiedBy: "set_reminder"}},
	}

	// Whatever the narrowing policy left behind — here, nothing at all.
	got := svc.ensureRequiredToolsVisible(nil, cfg)
	if !containsToolNamed(got, "set_reminder") {
		t.Fatalf("the required tool was not put back: %+v", toolNames(got))
	}

	// Already present: no duplicate.
	got = svc.ensureRequiredToolsVisible(got, cfg)
	if n := countToolNamed(got, "set_reminder"); n != 1 {
		t.Errorf("expected exactly one set_reminder, got %d", n)
	}

	// A run that forbids tools is owed nothing.
	forbid := DefaultRunConfig()
	forbid.resolvedConstraints = &RunConstraints{
		ForbidTools:      true,
		RequestedActions: []RequestedAction{{Kind: "reminder", SatisfiedBy: "set_reminder"}},
	}
	if got := svc.ensureRequiredToolsVisible(nil, forbid); len(got) != 0 {
		t.Errorf("a tools-forbidden run must stay empty, got %+v", toolNames(got))
	}

	// A conditional action is not enforced by the contract, but the branch
	// where its condition holds still needs the tool, so it stays visible.
	conditional := DefaultRunConfig()
	conditional.resolvedConstraints = &RunConstraints{
		RequestedActions: []RequestedAction{{Kind: "reminder", SatisfiedBy: "set_reminder", Unconditional: false}},
	}
	if got := svc.ensureRequiredToolsVisible(nil, conditional); !containsToolNamed(got, "set_reminder") {
		t.Errorf("a conditional action must still keep its tool reachable: %+v", toolNames(got))
	}

	// A tool nobody registered cannot be conjured up.
	unknown := DefaultRunConfig()
	unknown.resolvedConstraints = &RunConstraints{
		Deliverables: []DeliverableRequirement{{Kind: "email", SatisfiedBy: "send_email"}},
	}
	if got := svc.ensureRequiredToolsVisible(nil, unknown); len(got) != 0 {
		t.Errorf("an unregistered tool must not be invented, got %+v", toolNames(got))
	}
}

// An explicit denylist is the caller speaking directly, and still wins.
func TestRequiredToolStillObeysAnExplicitDenylist(t *testing.T) {
	t.Parallel()

	tools := []domain.ToolDefinition{
		{Type: "function", Function: domain.ToolFunction{Name: "set_reminder"}},
	}
	if got := filterTools(tools, nil, []string{"set_reminder"}); len(got) != 0 {
		t.Errorf("denylist ignored: %+v", toolNames(got))
	}
}

func toolNames(tools []domain.ToolDefinition) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func containsToolNamed(tools []domain.ToolDefinition, name string) bool {
	return countToolNamed(tools, name) > 0
}

func countToolNamed(tools []domain.ToolDefinition, name string) int {
	n := 0
	for _, t := range tools {
		if t.Function.Name == name {
			n++
		}
	}
	return n
}
