package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestConciergeBuiltInDelegationTargetsIntentRouterOnly(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	svc, err := manager.GetAgentService(BuiltInConciergeAgentName)
	if err != nil {
		t.Fatalf("get concierge service failed: %v", err)
	}
	if !svc.agent.HasTool("route_builtin_request") {
		t.Fatal("expected concierge to have route_builtin_request")
	}
	if svc.agent.HasTool("delegate_builtin_agent") {
		t.Fatal("did not expect concierge to expose delegate_builtin_agent directly")
	}
	if !svc.toolRegistry.Has("route_builtin_request") {
		t.Fatal("expected concierge tool registry to include route_builtin_request")
	}
}

func TestIntentRouterBuiltInDelegationTargetsCoreSpecialists(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	svc, err := manager.GetAgentService(BuiltInIntentRouterAgentName)
	if err != nil {
		t.Fatalf("get intent router service failed: %v", err)
	}
	if !svc.agent.HasTool("delegate_builtin_agent") {
		t.Fatal("expected intent router to have delegate_builtin_agent")
	}
	if !svc.agent.HasTool("submit_builtin_agent_task") {
		t.Fatal("expected intent router to have submit_builtin_agent_task")
	}

	raw, err := svc.toolRegistry.Call(context.Background(), "list_builtin_agents", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list_builtin_agents failed: %v", err)
	}
	agents, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("unexpected list_builtin_agents result: %#v", raw)
	}

	names := map[string]bool{}
	for _, item := range agents {
		if name, ok := item["name"].(string); ok {
			names[name] = true
		}
	}
	for _, want := range []string{
		defaultAssistantAgentName,
		defaultOperatorAgentName,
		defaultStakeholderAgentName,
		defaultArchivistAgentName,
		defaultVerifierAgentName,
	} {
		if !names[want] {
			t.Fatalf("expected IntentRouter delegable built-ins to include %s, got %+v", want, names)
		}
	}
	if names[defaultConciergeAgentName] {
		t.Fatalf("did not expect IntentRouter to delegate back to Concierge, got %+v", names)
	}
	if names[defaultIntentRouterAgentName] {
		t.Fatalf("did not expect IntentRouter to delegate to itself, got %+v", names)
	}
}
