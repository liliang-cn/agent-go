package agent

import (
	"path/filepath"
	"testing"
)

func TestTeamManagerAutoRegistersDispatcherLint(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	svc, err := manager.GetAgentService(BuiltInDispatcherAgentName)
	if err != nil {
		t.Fatalf("get dispatcher service: %v", err)
	}
	names := svc.OutputLints().Names(BuiltInDispatcherAgentName)
	if !containsString(names, "dispatcher_no_bounce_back") {
		t.Fatalf("Dispatcher service should auto-register dispatcher_no_bounce_back, got %v", names)
	}
}

func TestTeamManagerAutoRegistersArchivistLint(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	svc, err := manager.GetAgentService(defaultArchivistAgentName)
	if err != nil {
		t.Fatalf("get archivist service: %v", err)
	}
	names := svc.OutputLints().Names(defaultArchivistAgentName)
	if !containsString(names, "archivist_no_relative_time") {
		t.Fatalf("Archivist service should auto-register archivist_no_relative_time, got %v", names)
	}
}

func TestTeamManagerDoesNotAutoRegisterUnrelatedLints(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	// Operator should not pick up Dispatcher's or Archivist's lints just
	// because they share a TeamManager. Each lint stays scoped to its
	// owning agent.
	svc, err := manager.GetAgentService(defaultOperatorAgentName)
	if err != nil {
		t.Fatalf("get operator service: %v", err)
	}
	names := svc.OutputLints().Names(defaultOperatorAgentName)
	if containsString(names, "dispatcher_no_bounce_back") {
		t.Fatalf("Operator should NOT see dispatcher_no_bounce_back, got %v", names)
	}
	if containsString(names, "archivist_no_relative_time") {
		t.Fatalf("Operator should NOT see archivist_no_relative_time, got %v", names)
	}
}

// TestApplyBuiltInOutputLintsIsNilSafe pins the helper's defensive paths so
// that a future refactor that calls it with a nil service or model does
// not panic.
func TestApplyBuiltInOutputLintsIsNilSafe(t *testing.T) {
	applyBuiltInOutputLints(nil, &AgentModel{Name: BuiltInDispatcherAgentName})
	svc := &Service{}
	applyBuiltInOutputLints(svc, nil)
	applyBuiltInOutputLints(svc, &AgentModel{Name: ""})
	applyBuiltInOutputLints(svc, &AgentModel{Name: "SomeRandomAgent"})
	// none of the above should have panicked or registered anything
	names := svc.OutputLints().Names("SomeRandomAgent")
	if len(names) != 0 {
		t.Fatalf("unrelated agent should have no lints, got %v", names)
	}
}
