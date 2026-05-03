package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
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
	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	orchestrator, err := manager.GetMemberByName("Orchestrator")
	if err != nil {
		t.Fatalf("get default orchestrator failed: %v", err)
	}
	if orchestrator.Kind != AgentKindOrchestrator {
		t.Fatalf("expected orchestrator kind, got %q", orchestrator.Kind)
	}

	assistant, err := manager.GetAgentByName("Responder")
	if err != nil {
		t.Fatalf("get standalone assistant failed: %v", err)
	}
	if assistant.Kind != AgentKindAgent {
		t.Fatalf("expected Responder standalone kind, got %q", assistant.Kind)
	}
	if len(assistant.Teams) != 1 {
		t.Fatalf("expected Responder to be in default team, got teams=%+v", assistant.Teams)
	}
	if assistant.Teams[0].Role != AgentKindSpecialist {
		t.Fatalf("expected Responder role specialist, got %q", assistant.Teams[0].Role)
	}

	operator, err := manager.GetAgentByName("Operator")
	if err != nil {
		t.Fatalf("get standalone operator failed: %v", err)
	}
	if operator.Kind != AgentKindAgent {
		t.Fatalf("expected Operator standalone kind, got %q", operator.Kind)
	}
	if len(operator.Teams) != 1 {
		t.Fatalf("expected Operator to be in default team, got teams=%+v", operator.Teams)
	}
	if operator.Teams[0].Role != AgentKindSpecialist {
		t.Fatalf("expected Operator role specialist, got %q", operator.Teams[0].Role)
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
	if len(operator.MCPTools) != 1 || operator.MCPTools[0] != "*" {
		t.Fatalf("expected Operator to receive wildcard MCP allowlist, got %v", operator.MCPTools)
	}

	dispatcher, err := manager.GetAgentByName("Dispatcher")
	if err != nil {
		t.Fatalf("get standalone dispatcher failed: %v", err)
	}
	if dispatcher.Kind != AgentKindAgent {
		t.Fatalf("expected Dispatcher standalone kind, got %q", dispatcher.Kind)
	}
	if len(dispatcher.Teams) != 1 {
		t.Fatalf("expected Dispatcher to be in default team, got teams=%+v", dispatcher.Teams)
	}
	if dispatcher.Teams[0].Role != AgentKindSpecialist {
		t.Fatalf("expected Dispatcher role specialist, got %q", dispatcher.Teams[0].Role)
	}
	if dispatcher.Description != "Always-on user entry agent for intake, status checks, and dispatching work." {
		t.Fatalf("unexpected Dispatcher description: %q", dispatcher.Description)
	}
	if !strings.Contains(dispatcher.Instructions, "only job is intake, routing, status inspection, task planning, and task dispatch") ||
		!strings.Contains(dispatcher.Instructions, "call route_builtin_request") ||
		!strings.Contains(dispatcher.Instructions, "runs PromptOptimizer and IntentRouter in parallel") ||
		!strings.Contains(dispatcher.Instructions, "task_plan_create") ||
		!strings.Contains(dispatcher.Instructions, "Do not use submit_agent_task or submit_team_task for ordinary user requests") {
		t.Fatalf("expected Dispatcher prompt to focus on dispatch-only routing, got %q", dispatcher.Instructions)
	}
	if !dispatcher.EnableMemory {
		t.Fatal("expected Dispatcher to keep long-term memory enabled")
	}
	if dispatcher.EnableMCP || len(dispatcher.MCPTools) != 0 {
		t.Fatalf("expected Dispatcher to stay lightweight without default MCP tools, got enable_mcp=%v tools=%v", dispatcher.EnableMCP, dispatcher.MCPTools)
	}

	intentRouter, err := manager.GetAgentByName(defaultIntentRouterAgentName)
	if err != nil {
		t.Fatalf("get standalone intent router failed: %v", err)
	}
	if intentRouter.Kind != AgentKindAgent {
		t.Fatalf("expected IntentRouter standalone kind, got %q", intentRouter.Kind)
	}
	if len(intentRouter.Teams) != 1 {
		t.Fatalf("expected IntentRouter to be in default team, got teams=%+v", intentRouter.Teams)
	}
	if intentRouter.Teams[0].Role != AgentKindSpecialist {
		t.Fatalf("expected IntentRouter role specialist, got %q", intentRouter.Teams[0].Role)
	}
	if intentRouter.Description != defaultIntentRouterAgentDescription {
		t.Fatalf("unexpected IntentRouter description: %q", intentRouter.Description)
	}
	if !strings.Contains(intentRouter.Instructions, "use the LLM to classify the user's request") ||
		!strings.Contains(intentRouter.Instructions, "choose the single best-fit built-in standalone agent") {
		t.Fatalf("expected IntentRouter prompt to focus on intent recognition and delegation, got %q", intentRouter.Instructions)
	}
	if intentRouter.EnableMCP || len(intentRouter.MCPTools) != 0 {
		t.Fatalf("expected IntentRouter to stay lightweight without default MCP tools, got enable_mcp=%v tools=%v", intentRouter.EnableMCP, intentRouter.MCPTools)
	}

	promptOptimizer, err := manager.GetAgentByName(defaultPromptOptimizerAgentName)
	if err != nil {
		t.Fatalf("get standalone prompt optimizer failed: %v", err)
	}
	if promptOptimizer.Kind != AgentKindAgent {
		t.Fatalf("expected PromptOptimizer standalone kind, got %q", promptOptimizer.Kind)
	}
	if len(promptOptimizer.Teams) != 1 {
		t.Fatalf("expected PromptOptimizer to be in default team, got teams=%+v", promptOptimizer.Teams)
	}
	if promptOptimizer.Teams[0].Role != AgentKindSpecialist {
		t.Fatalf("expected PromptOptimizer role specialist, got %q", promptOptimizer.Teams[0].Role)
	}
	if promptOptimizer.Description != defaultPromptOptimizerAgentDescription {
		t.Fatalf("unexpected PromptOptimizer description: %q", promptOptimizer.Description)
	}
	if !strings.Contains(promptOptimizer.Instructions, "rewrite a user's request into a clearer downstream instruction") {
		t.Fatalf("expected PromptOptimizer prompt to focus on prompt rewriting, got %q", promptOptimizer.Instructions)
	}
	if promptOptimizer.EnableMCP || len(promptOptimizer.MCPTools) != 0 {
		t.Fatalf("expected PromptOptimizer to stay lightweight without default MCP tools, got enable_mcp=%v tools=%v", promptOptimizer.EnableMCP, promptOptimizer.MCPTools)
	}

	verifier, err := manager.GetAgentByName("Verifier")
	if err != nil {
		t.Fatalf("get standalone verifier failed: %v", err)
	}
	if !verifier.EnableMCP || len(verifier.MCPTools) != 1 || verifier.MCPTools[0] != "*" {
		t.Fatalf("expected Verifier to have wildcard MCP verification access, got enable_mcp=%v tools=%v", verifier.EnableMCP, verifier.MCPTools)
	}
	if !strings.Contains(verifier.Instructions, "Do not repeat the primary action unless verification genuinely requires it") {
		t.Fatalf("expected Verifier instructions to emphasize independent verification, got %q", verifier.Instructions)
	}

	evaluator, err := manager.GetAgentByName("Evaluator")
	if err != nil {
		t.Fatalf("get standalone evaluator failed: %v", err)
	}
	if evaluator.Kind != AgentKindAgent {
		t.Fatalf("expected Evaluator standalone kind, got %q", evaluator.Kind)
	}
	if len(evaluator.Teams) != 1 {
		t.Fatalf("expected Evaluator to be in default team, got teams=%+v", evaluator.Teams)
	}
	if evaluator.Teams[0].Role != AgentKindSpecialist {
		t.Fatalf("expected Evaluator role specialist, got %q", evaluator.Teams[0].Role)
	}
	if evaluator.Description != "Product/business representative for goals, scope, priorities, and acceptance criteria." {
		t.Fatalf("unexpected Evaluator description: %q", evaluator.Description)
	}
	if !strings.Contains(evaluator.Instructions, "product manager or business representative") {
		t.Fatalf("expected Evaluator prompt to include PM/business framing, got %q", evaluator.Instructions)
	}
	if !strings.Contains(evaluator.Instructions, "Do not write code unless the user explicitly asks you to") {
		t.Fatalf("expected Evaluator prompt to discourage direct coding, got %q", evaluator.Instructions)
	}
	if !strings.Contains(evaluator.Instructions, "acceptance criteria") || !strings.Contains(evaluator.Instructions, "risk lists") || !strings.Contains(evaluator.Instructions, "prioritization recommendations") {
		t.Fatalf("expected Evaluator prompt to prioritize product outputs, got %q", evaluator.Instructions)
	}

	if _, err := manager.GetMemberByName("Coder"); err == nil {
		t.Fatal("expected default team to not seed Coder")
	}

	if _, err := manager.GetMemberByName("FileSystemAgent"); err == nil {
		t.Fatal("expected FileSystemAgent to be removed from the default team")
	}
}

func TestCreateMemberAppliesUsefulDefaults(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
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
		Name:         "DocOrchestrator",
		TeamID:       "docs-team-test",
		Kind:         AgentKindOrchestrator,
		Description:  "Leads documentation work.",
		Instructions: "Coordinate documentation tasks.",
	}); err == nil {
		t.Fatalf("expected unknown team creation to fail")
	}

	team, err := manager.CreateTeam(context.Background(), &Team{
		Name:        "Docs Team",
		Description: "Documentation team.",
	})
	if err != nil {
		t.Fatalf("create team failed: %v", err)
	}

	docsMember, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "DocWriter",
		TeamID:       team.ID,
		Kind:         AgentKindSpecialist,
		Description:  "Writes documentation.",
		Instructions: "Write documentation.",
	})
	if err != nil {
		t.Fatalf("create specialist in new team failed: %v", err)
	}
	if !docsMember.EnableMCP || !docsMember.EnableRAG || !docsMember.EnableMemory {
		t.Fatalf("expected team member defaults to enable MCP/RAG/Memory, got %+v", docsMember)
	}
}

func TestCreateMemberOrchestratorConflictDoesNotLeaveStandaloneAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	team, err := manager.CreateTeam(context.Background(), &Team{
		Name:        "Docs Team",
		Description: "Documentation team.",
	})
	if err != nil {
		t.Fatalf("create team failed: %v", err)
	}

	_, err = manager.CreateMember(context.Background(), &AgentModel{
		Name:         "Docs PM",
		TeamID:       team.ID,
		Kind:         AgentKindOrchestrator,
		Description:  "duplicate lead",
		Instructions: "duplicate lead",
	})
	if err == nil {
		t.Fatal("expected duplicate team orchestrator creation to fail")
	}

	if _, getErr := manager.GetAgentByName("Docs PM"); getErr == nil {
		t.Fatal("expected failed orchestrator creation to not leave a standalone agent behind")
	}
}

func TestCreateAgentCreatesStandaloneAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
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
		t.Fatalf("expected standalone agent to have no team, got %q", model.TeamID)
	}

	members, err := manager.ListMembers()
	if err != nil {
		t.Fatalf("list members failed: %v", err)
	}
	for _, member := range members {
		if member.Name == "Writer" {
			t.Fatalf("expected standalone agent to be excluded from team members: %+v", member)
		}
	}
}

func TestCustomStandaloneAgentCanDelegateBuiltInAgents(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("AGENTGO_HOME", tmpDir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		t.Fatalf("new agentgo db failed: %v", err)
	}
	defer db.Close()
	if err := db.SaveConfig("llm.strategy", "round_robin"); err != nil {
		t.Fatalf("save llm.strategy failed: %v", err)
	}
	if err := db.SaveProvider(&store.LLMProvider{
		Name:           "local",
		BaseURL:        "http://localhost:8080",
		Key:            "test",
		ModelName:      "gpt-test",
		MaxConcurrency: 1,
		Capability:     1,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("save provider failed: %v", err)
	}

	store, err := NewStore(filepath.Join(tmpDir, "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
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
	if !names["Operator"] || !names["Responder"] || !names["Evaluator"] {
		t.Fatalf("expected delegable built-in agents to include Operator, Responder, Evaluator, got %+v", names)
	}
	if names[defaultIntentRouterAgentName] {
		t.Fatalf("did not expect IntentRouter to be delegable for custom agents, got %+v", names)
	}
	if names["Dispatcher"] {
		t.Fatalf("did not expect Dispatcher to be delegable, got %+v", names)
	}

	prompt := svc.agent.Instructions()
	if !strings.Contains(prompt, "Delegable system built-in agents you may use in addition to your own role and capabilities:") {
		t.Fatalf("expected built-in agent delegation prompt context, got %q", prompt)
	}
	if !strings.Contains(prompt, "- Operator:") {
		t.Fatalf("expected Operator in built-in delegation prompt, got %q", prompt)
	}
}

func TestJoinAndLeaveTeamMovesStandaloneAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
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

	joined, err := manager.JoinTeam(context.Background(), writer.Name, defaultTeamID, AgentKindSpecialist)
	if err != nil {
		t.Fatalf("join team failed: %v", err)
	}
	if joined.TeamID != defaultTeamID {
		t.Fatalf("expected joined team id %q, got %q", defaultTeamID, joined.TeamID)
	}
	if joined.Kind != AgentKindSpecialist {
		t.Fatalf("expected joined kind specialist, got %q", joined.Kind)
	}
	if _, err := manager.GetMemberByName(writer.Name); err != nil {
		t.Fatalf("expected joined agent to be loadable as member: %v", err)
	}

	left, err := manager.LeaveTeam(context.Background(), writer.Name)
	if err != nil {
		t.Fatalf("leave team failed: %v", err)
	}
	if left.TeamID != "" {
		t.Fatalf("expected agent to leave team, got %q", left.TeamID)
	}
	if left.Kind != AgentKindAgent {
		t.Fatalf("expected standalone kind agent after leave, got %q", left.Kind)
	}
	if _, err := manager.GetMemberByName(writer.Name); err == nil {
		t.Fatal("expected standalone agent to no longer be treated as team member")
	}
}

func TestLastOrchestratorCannotLeaveOrBeDeleted(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	if _, err := manager.LeaveTeam(context.Background(), "Orchestrator"); err == nil {
		t.Fatal("expected last orchestrator leave to fail")
	}
	if err := manager.DeleteAgent(context.Background(), "Orchestrator"); err == nil {
		t.Fatal("expected last orchestrator delete to fail")
	}
}
