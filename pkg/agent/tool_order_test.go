package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// The tool list is part of the request prefix a prompt cache matches on, so an
// order that varies between rounds costs a run its cache on every turn. Ranging
// a Go map did exactly that. Assert the order is fixed.
func TestCollectToolsOrderIsStableAcrossCalls(t *testing.T) {
	reg := NewToolRegistry()
	for i := range 24 {
		name := fmt.Sprintf("tool_%02d", i)
		reg.Register(domain.ToolDefinition{
			Type:     "function",
			Function: domain.ToolFunction{Name: name, Description: "d"},
		}, func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, "test")
	}

	svc := &Service{toolRegistry: reg}
	policy := toolPreparationPolicy{SessionID: "s1"}

	first := svc.collectTools(context.Background(), nil, policy, false)
	if len(first) != 24 {
		t.Fatalf("expected 24 tools, got %d", len(first))
	}
	for range 20 {
		got := svc.collectTools(context.Background(), nil, policy, false)
		if len(got) != len(first) {
			t.Fatalf("tool count changed: %d then %d", len(first), len(got))
		}
		for i := range got {
			if got[i].Function.Name != first[i].Function.Name {
				t.Fatalf("tool order changed at %d: %q then %q",
					i, first[i].Function.Name, got[i].Function.Name)
			}
		}
	}

	// And it is sorted, not merely repeatable, so two processes agree too.
	for i := 1; i < len(first); i++ {
		if first[i-1].Function.Name >= first[i].Function.Name {
			t.Fatalf("tools not sorted: %q before %q",
				first[i-1].Function.Name, first[i].Function.Name)
		}
	}
}
