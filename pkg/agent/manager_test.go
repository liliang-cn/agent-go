package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeDispatchText(t *testing.T) {
	got := sanitizeDispatchText("<think>internal reasoning</think>\n\nFinal answer")
	if got != "Final answer" {
		t.Fatalf("expected thinking tags to be removed, got %q", got)
	}
}

func TestNewStoreAppliesSQLitePragmas(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	defer store.GetAgentGoDB().GetDB().Close()

	var journalMode string
	if err := store.GetAgentGoDB().GetDB().QueryRow(`PRAGMA journal_mode;`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode failed: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("expected WAL mode, got %q", journalMode)
	}

	var busyTimeout int
	if err := store.GetAgentGoDB().GetDB().QueryRow(`PRAGMA busy_timeout;`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout failed: %v", err)
	}
	if busyTimeout < sqliteBusyTimeoutMillis {
		t.Fatalf("expected busy_timeout >= %d, got %d", sqliteBusyTimeoutMillis, busyTimeout)
	}
}

func TestSeedDefaultMembersCreatesBuiltInsByDefault(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	captain, err := manager.GetMemberByName("Captain")
	if err != nil {
		t.Fatalf("get default captain failed: %v", err)
	}
	if captain.Kind != AgentKindCaptain {
		t.Fatalf("expected captain kind, got %q", captain.Kind)
	}

	assistant, err := manager.GetAgentByName("Assistant")
	if err != nil {
		t.Fatalf("get standalone assistant failed: %v", err)
	}
	if assistant.Kind != AgentKindAgent {
		t.Fatalf("expected Assistant standalone kind, got %q", assistant.Kind)
	}
	if len(assistant.Squads) != 0 {
		t.Fatalf("expected Assistant to be standalone, got squads=%+v", assistant.Squads)
	}

	operator, err := manager.GetAgentByName("Operator")
	if err != nil {
		t.Fatalf("get standalone operator failed: %v", err)
	}
	if operator.Kind != AgentKindAgent {
		t.Fatalf("expected Operator standalone kind, got %q", operator.Kind)
	}
	if len(operator.Squads) != 0 {
		t.Fatalf("expected Operator to be standalone, got squads=%+v", operator.Squads)
	}
	if operator.Description != "An execution-focused standalone operator for file work, environment checks, and runnable validation steps." {
		t.Fatalf("unexpected Operator description: %q", operator.Description)
	}
	if !strings.Contains(operator.Instructions, "execution-focused agent") || !strings.Contains(operator.Instructions, "operational work directly") {
		t.Fatalf("expected Operator prompt to focus on direct execution, got %q", operator.Instructions)
	}
	if !operator.EnableMCP || len(operator.MCPTools) == 0 {
		t.Fatalf("expected Operator to have MCP enabled with default tools, got enable_mcp=%v tools=%v", operator.EnableMCP, operator.MCPTools)
	}

	concierge, err := manager.GetAgentByName("Concierge")
	if err != nil {
		t.Fatalf("get standalone concierge failed: %v", err)
	}
	if concierge.Kind != AgentKindAgent {
		t.Fatalf("expected Concierge standalone kind, got %q", concierge.Kind)
	}
	if len(concierge.Squads) != 0 {
		t.Fatalf("expected Concierge to be standalone, got squads=%+v", concierge.Squads)
	}
	if concierge.Description != "Always-on user entry agent for intake, status checks, and dispatching work." {
		t.Fatalf("unexpected Concierge description: %q", concierge.Description)
	}
	if !strings.Contains(concierge.Instructions, "only job is intake, routing, status inspection, and task dispatch") ||
		!strings.Contains(concierge.Instructions, "delegate to Archivist") ||
		!strings.Contains(concierge.Instructions, "delegate to Verifier") {
		t.Fatalf("expected Concierge prompt to focus on dispatch-only routing, got %q", concierge.Instructions)
	}
	if concierge.EnableMCP || len(concierge.MCPTools) != 0 {
		t.Fatalf("expected Concierge to stay lightweight without default MCP tools, got enable_mcp=%v tools=%v", concierge.EnableMCP, concierge.MCPTools)
	}

	stakeholder, err := manager.GetAgentByName("Stakeholder")
	if err != nil {
		t.Fatalf("get standalone stakeholder failed: %v", err)
	}
	if stakeholder.Kind != AgentKindAgent {
		t.Fatalf("expected Stakeholder standalone kind, got %q", stakeholder.Kind)
	}
	if len(stakeholder.Squads) != 0 {
		t.Fatalf("expected Stakeholder to be standalone, got squads=%+v", stakeholder.Squads)
	}
	if stakeholder.Description != "Product/business representative for goals, scope, priorities, and acceptance criteria." {
		t.Fatalf("unexpected Stakeholder description: %q", stakeholder.Description)
	}
	if !strings.Contains(stakeholder.Instructions, "product manager or business representative") {
		t.Fatalf("expected Stakeholder prompt to include PM/business framing, got %q", stakeholder.Instructions)
	}
	if !strings.Contains(stakeholder.Instructions, "Do not write code unless the user explicitly asks you to") {
		t.Fatalf("expected Stakeholder prompt to discourage direct coding, got %q", stakeholder.Instructions)
	}
	if !strings.Contains(stakeholder.Instructions, "acceptance criteria") || !strings.Contains(stakeholder.Instructions, "risk lists") || !strings.Contains(stakeholder.Instructions, "prioritization recommendations") {
		t.Fatalf("expected Stakeholder prompt to prioritize product outputs, got %q", stakeholder.Instructions)
	}

	if _, err := manager.GetMemberByName("Coder"); err == nil {
		t.Fatal("expected default squad to not seed Coder")
	}

	if _, err := manager.GetMemberByName("FileSystemAgent"); err == nil {
		t.Fatal("expected FileSystemAgent to be removed from the default squad")
	}
}

func TestCreateMemberAppliesUsefulDefaults(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	writer, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "Writer",
		Kind:         AgentKindSpecialist,
		Description:  "Writes concise docs.",
		Instructions: "Write concise docs.",
	})
	if err != nil {
		t.Fatalf("create specialist failed: %v", err)
	}
	if !writer.EnableMCP {
		t.Fatal("expected created member to enable MCP by default")
	}
	if len(writer.MCPTools) == 0 {
		t.Fatal("expected created member to receive default MCP tools")
	}

	if _, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "DocCaptain",
		TeamID:       "docs-squad-test",
		Kind:         AgentKindCaptain,
		Description:  "Leads documentation work.",
		Instructions: "Coordinate documentation tasks.",
	}); err == nil {
		t.Fatalf("expected unknown squad creation to fail")
	}

	squad, err := manager.CreateSquad(context.Background(), &Squad{
		Name:        "Docs Squad",
		Description: "Documentation squad.",
	})
	if err != nil {
		t.Fatalf("create squad failed: %v", err)
	}

	docsMember, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "DocWriter",
		TeamID:       squad.ID,
		Kind:         AgentKindSpecialist,
		Description:  "Writes documentation.",
		Instructions: "Write documentation.",
	})
	if err != nil {
		t.Fatalf("create specialist in new squad failed: %v", err)
	}
	if !docsMember.EnableMCP || !docsMember.EnableRAG || !docsMember.EnableMemory {
		t.Fatalf("expected squad member defaults to enable MCP/RAG/Memory, got %+v", docsMember)
	}
}

func TestCreateMemberCaptainConflictDoesNotLeaveStandaloneAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	squad, err := manager.CreateSquad(context.Background(), &Squad{
		Name:        "Docs Squad",
		Description: "Documentation squad.",
	})
	if err != nil {
		t.Fatalf("create squad failed: %v", err)
	}

	_, err = manager.CreateMember(context.Background(), &AgentModel{
		Name:         "Docs PM",
		TeamID:       squad.ID,
		Kind:         AgentKindCaptain,
		Description:  "duplicate lead",
		Instructions: "duplicate lead",
	})
	if err == nil {
		t.Fatal("expected duplicate squad captain creation to fail")
	}

	if _, getErr := manager.GetAgentByName("Docs PM"); getErr == nil {
		t.Fatal("expected failed captain creation to not leave a standalone agent behind")
	}
}

func TestCreateAgentCreatesStandaloneAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	model, err := manager.CreateAgent(context.Background(), &AgentModel{
		Name:         "Writer",
		Description:  "Writes standalone notes.",
		Instructions: "Write concise standalone notes.",
	})
	if err != nil {
		t.Fatalf("create agent failed: %v", err)
	}
	if model.Kind != AgentKindAgent {
		t.Fatalf("expected standalone kind agent, got %q", model.Kind)
	}
	if model.TeamID != "" {
		t.Fatalf("expected standalone agent to have no squad, got %q", model.TeamID)
	}

	members, err := manager.ListMembers()
	if err != nil {
		t.Fatalf("list members failed: %v", err)
	}
	for _, member := range members {
		if member.Name == "Writer" {
			t.Fatalf("expected standalone agent to be excluded from squad members: %+v", member)
		}
	}
}

func TestCustomStandaloneAgentCanDelegateBuiltInAgents(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agentgo.toml")
	if err := os.WriteFile(configPath, []byte(`
home = "`+tmpDir+`"

[llm]
enabled = true
strategy = "round_robin"

[[llm.providers]]
name = "local"
base_url = "http://localhost:8080"
key = "test"
model_name = "gpt-test"
max_concurrency = 1
capability = 1

[rag]
enabled = false

[mcp]
enabled = true
`), 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	store, err := NewStore(filepath.Join(tmpDir, "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	model, err := manager.CreateAgent(context.Background(), &AgentModel{
		Name:         "Reviewer",
		Description:  "Reviews outputs.",
		Instructions: "Review outputs and escalate when needed.",
	})
	if err != nil {
		t.Fatalf("create agent failed: %v", err)
	}
	if model.Kind != AgentKindAgent {
		t.Fatalf("expected standalone kind agent, got %q", model.Kind)
	}

	svc, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("get agent service failed: %v", err)
	}

	if !svc.agent.HasTool("delegate_builtin_agent") {
		t.Fatal("expected custom agent to have delegate_builtin_agent")
	}
	if !svc.agent.HasTool("submit_builtin_agent_task") {
		t.Fatal("expected custom agent to have submit_builtin_agent_task")
	}
	if !svc.agent.HasTool("get_delegated_task_status") {
		t.Fatal("expected custom agent to have get_delegated_task_status")
	}
	if !svc.agent.HasTool("list_builtin_agents") {
		t.Fatal("expected custom agent to have list_builtin_agents")
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
	if !names["Operator"] || !names["Assistant"] || !names["Stakeholder"] {
		t.Fatalf("expected delegable built-in agents to include Operator, Assistant, Stakeholder, got %+v", names)
	}
	if names["Concierge"] {
		t.Fatalf("did not expect Concierge to be delegable, got %+v", names)
	}

	prompt := svc.agent.Instructions()
	if !strings.Contains(prompt, "Delegable system built-in agents you may use in addition to your own role and capabilities:") {
		t.Fatalf("expected built-in agent delegation prompt context, got %q", prompt)
	}
	if !strings.Contains(prompt, "- Operator:") {
		t.Fatalf("expected Operator in built-in delegation prompt, got %q", prompt)
	}
}

func TestJoinAndLeaveSquadMovesStandaloneAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	writer, err := manager.CreateAgent(context.Background(), &AgentModel{
		Name:         "Writer",
		Description:  "Writes docs.",
		Instructions: "Write docs.",
	})
	if err != nil {
		t.Fatalf("create standalone agent failed: %v", err)
	}

	joined, err := manager.JoinSquad(context.Background(), writer.Name, defaultSquadID, AgentKindSpecialist)
	if err != nil {
		t.Fatalf("join squad failed: %v", err)
	}
	if joined.TeamID != defaultSquadID {
		t.Fatalf("expected joined squad id %q, got %q", defaultSquadID, joined.TeamID)
	}
	if joined.Kind != AgentKindSpecialist {
		t.Fatalf("expected joined kind specialist, got %q", joined.Kind)
	}
	if _, err := manager.GetMemberByName(writer.Name); err != nil {
		t.Fatalf("expected joined agent to be loadable as member: %v", err)
	}

	left, err := manager.LeaveSquad(context.Background(), writer.Name)
	if err != nil {
		t.Fatalf("leave squad failed: %v", err)
	}
	if left.TeamID != "" {
		t.Fatalf("expected agent to leave squad, got %q", left.TeamID)
	}
	if left.Kind != AgentKindAgent {
		t.Fatalf("expected standalone kind agent after leave, got %q", left.Kind)
	}
	if _, err := manager.GetMemberByName(writer.Name); err == nil {
		t.Fatal("expected standalone agent to no longer be treated as squad member")
	}
}

func TestLastCaptainCannotLeaveOrBeDeleted(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	if _, err := manager.LeaveSquad(context.Background(), "Captain"); err == nil {
		t.Fatal("expected last captain leave to fail")
	}
	if err := manager.DeleteAgent(context.Background(), "Captain"); err == nil {
		t.Fatal("expected last captain delete to fail")
	}
}
