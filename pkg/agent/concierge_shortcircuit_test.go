package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestExecuteDirectConciergeRouteShortCircuitsThroughToolRegistry(t *testing.T) {
	svc := &Service{
		agent:        NewAgentWithConfig(BuiltInConciergeAgentName, "concierge", nil),
		toolRegistry: NewToolRegistry(),
	}
	svc.SetSessionID("session-1")

	called := false
	svc.toolRegistry.Register(toolDef("route_builtin_request"), func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		called = true
		return map[string]interface{}{
			"target_agent":        "Operator",
			"intent_type":         "tool_execution",
			"routing_reason":      "execution request",
			"optimized_prompt":    "让宠物狗跑起来",
			"result":              "Desktop pet walking started.",
			"verification_result": "VERIFIED_COMPLETE: Desktop pet walking started.",
		}, nil
	}, CategoryCustom)

	session := NewSessionWithID("session-1", svc.agent.ID())
	result, ok, err := svc.executeDirectConciergeRoute(context.Background(), session, "让宠物狗跑起来")
	if err != nil {
		t.Fatalf("executeDirectConciergeRoute failed: %v", err)
	}
	if !ok {
		t.Fatal("expected direct concierge route to trigger")
	}
	if !called {
		t.Fatal("expected route_builtin_request to be invoked")
	}
	if result.Text() != "Desktop pet walking started." {
		t.Fatalf("unexpected result text: %+v", result)
	}
	if got := metadataString(result.Metadata, "dispatch_target"); got != "Operator" {
		t.Fatalf("expected Operator dispatch target, got %q", got)
	}
}

func toolDef(name string) domain.ToolDefinition {
	return domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:       name,
			Parameters: map[string]interface{}{"type": "object"},
		},
	}
}
