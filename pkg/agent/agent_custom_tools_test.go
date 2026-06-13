package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRegisterAgentToolAttachesClosureAndRebuilds(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	manager.SetLLM(&serviceExecutionStateTestLLM{})

	model := &AgentModel{
		ID:           "agent-closure-tool",
		A2AID:        "a2a-closure-tool",
		Name:         "ClosureToolAgent",
		Kind:         AgentKindAgent,
		Description:  "agent with a closure tool",
		Instructions: "use the tool",
		EnablePTC:    false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := store.SaveAgentModel(model); err != nil {
		t.Fatalf("SaveAgentModel() error = %v", err)
	}

	// Before registration: tool absent.
	svc1, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() error = %v", err)
	}
	if svc1.toolRegistry.Has("lookup_kb") {
		t.Fatal("did not expect lookup_kb before registration")
	}

	// Register a Go closure tool by agent name.
	called := false
	err = manager.RegisterAgentTool("ClosureToolAgent", "lookup_kb", "Search the KB",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"query": map[string]interface{}{"type": "string"}},
		},
		func(ctx context.Context, a map[string]interface{}) (interface{}, error) {
			called = true
			return map[string]interface{}{"ok": true, "data": "hit"}, nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true},
	)
	if err != nil {
		t.Fatalf("RegisterAgentTool() error = %v", err)
	}

	// Cache must have been invalidated.
	manager.mu.RLock()
	_, cached := manager.services["ClosureToolAgent"]
	manager.mu.RUnlock()
	if cached {
		t.Fatal("expected cached service to be dropped after RegisterAgentTool")
	}

	// After registration + rebuild: tool present and callable.
	svc2, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() rebuild error = %v", err)
	}
	if svc1 == svc2 {
		t.Fatal("expected a rebuilt service after RegisterAgentTool")
	}
	if !svc2.toolRegistry.Has("lookup_kb") {
		t.Fatal("expected lookup_kb on the rebuilt service")
	}

	// Invoke the registered handler to prove the closure is wired.
	if _, err := svc2.toolRegistry.Call(context.Background(), "lookup_kb", map[string]interface{}{"query": "x"}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !called {
		t.Fatal("expected closure handler to be invoked")
	}
}

func TestRegisterAgentToolValidation(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	manager := NewTeamManager(store)
	if err := manager.RegisterAgentTool("", "t", "d", nil, func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil }, ToolMetadata{}); err == nil {
		t.Fatal("expected error for empty agent name")
	}
	if err := manager.RegisterAgentTool("A", "t", "d", nil, nil, ToolMetadata{}); err == nil {
		t.Fatal("expected error for nil handler")
	}
}
