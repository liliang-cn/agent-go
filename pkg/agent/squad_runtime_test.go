package agent

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestGetSquadStatusIdleByDefault(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	status, err := manager.GetSquadStatus(defaultSquadID)
	if err != nil {
		t.Fatalf("get squad status failed: %v", err)
	}
	if status.Status != "idle" {
		t.Fatalf("expected idle status, got %q", status.Status)
	}
	if status.RunningTasks != 0 || status.QueuedTasks != 0 {
		t.Fatalf("expected no active tasks, got running=%d queued=%d", status.RunningTasks, status.QueuedTasks)
	}
}

func TestGetSquadStatusAggregatesRunningAndQueuedTasks(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	manager.queueMu.Lock()
	manager.sharedTasks["task-running"] = &SharedTask{
		ID:          "task-running",
		SquadID:     defaultSquadID,
		CaptainName: "Captain",
		Status:      SharedTaskStatusRunning,
	}
	manager.sharedTasks["task-queued"] = &SharedTask{
		ID:          "task-queued",
		SquadID:     defaultSquadID,
		CaptainName: "Captain",
		Status:      SharedTaskStatusQueued,
	}
	manager.queueRunning[defaultSquadID] = true
	manager.queueMu.Unlock()

	status, err := manager.GetSquadStatus(defaultSquadID)
	if err != nil {
		t.Fatalf("get squad status failed: %v", err)
	}
	if status.Status != "running" {
		t.Fatalf("expected running status, got %q", status.Status)
	}
	if status.RunningTasks != 1 {
		t.Fatalf("expected 1 running task, got %d", status.RunningTasks)
	}
	if status.QueuedTasks != 1 {
		t.Fatalf("expected 1 queued task, got %d", status.QueuedTasks)
	}
	if len(status.ActiveTaskIDs) != 1 || status.ActiveTaskIDs[0] != "task-running" {
		t.Fatalf("unexpected active task ids: %+v", status.ActiveTaskIDs)
	}
	if len(status.RunningCaptains) != 1 || status.RunningCaptains[0] != "Captain" {
		t.Fatalf("unexpected running captains: %+v", status.RunningCaptains)
	}
}

func TestGetAgentStatusIncludesConcierge(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	status, err := manager.GetAgentStatus(BuiltInConciergeAgentName)
	if err != nil {
		t.Fatalf("get concierge status failed: %v", err)
	}
	if status.Name != BuiltInConciergeAgentName {
		t.Fatalf("unexpected concierge status name: %+v", status)
	}
	if !status.BuiltIn {
		t.Fatalf("expected concierge to be built-in: %+v", status)
	}
	if status.Status != "idle" {
		t.Fatalf("expected idle concierge, got %q", status.Status)
	}
	if len(status.Squads) != 0 {
		t.Fatalf("expected concierge to remain standalone, got squads=%+v", status.Squads)
	}
}

func TestRegisterConciergeToolsExposesStatusAndSubmission(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	manager.queueMu.Lock()
	manager.queueRunning[defaultSquadID] = true
	manager.queueMu.Unlock()

	svc := &Service{
		agent:        NewAgentWithConfig(BuiltInConciergeAgentName, "concierge", nil),
		toolRegistry: NewToolRegistry(),
	}
	svc.toolRegistry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "delegate_to_subagent",
			Description: "delegation helper",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)
	svc.toolRegistry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "search_available_tools",
			Description: "search helper",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)
	svc.toolRegistry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "memory_recall",
			Description: "memory helper",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryMemory)
	svc.agent.AddTool("delegate_to_subagent", "delegation helper", map[string]interface{}{"type": "object"}, nil)
	svc.agent.AddTool("search_available_tools", "search helper", map[string]interface{}{"type": "object"}, nil)
	svc.agent.AddTool("memory_recall", "memory helper", map[string]interface{}{"type": "object"}, nil)
	manager.RegisterConciergeTools(svc)

	if svc.toolRegistry.Has("delegate_to_subagent") {
		t.Fatal("expected delegate_to_subagent to be removed from Concierge tool registry")
	}
	if svc.toolRegistry.Has("search_available_tools") {
		t.Fatal("expected search_available_tools to be removed from Concierge tool registry")
	}
	if svc.toolRegistry.Has("memory_recall") {
		t.Fatal("expected memory_recall to be removed from Concierge tool registry in file-only mode")
	}
	if svc.agent.HasTool("delegate_to_subagent") {
		t.Fatal("expected delegate_to_subagent to be removed from Concierge agent tools")
	}
	if svc.agent.HasTool("search_available_tools") {
		t.Fatal("expected search_available_tools to be removed from Concierge agent tools")
	}
	if svc.agent.HasTool("memory_recall") {
		t.Fatal("expected memory_recall to be removed from Concierge agent tools in file-only mode")
	}
	if !svc.agent.HasTool("submit_agent_task") {
		t.Fatal("expected submit_agent_task to be available on Concierge")
	}

	rawSquads, err := svc.toolRegistry.Call(context.Background(), "list_squads", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list_squads failed: %v", err)
	}
	squads, ok := rawSquads.([]map[string]interface{})
	if !ok || len(squads) == 0 {
		t.Fatalf("unexpected squad tool result: %#v", rawSquads)
	}

	rawAgentStatus, err := svc.toolRegistry.Call(context.Background(), "get_agent_status", map[string]interface{}{
		"agent_name": BuiltInConciergeAgentName,
	})
	if err != nil {
		t.Fatalf("get_agent_status failed: %v", err)
	}
	agentStatus, ok := rawAgentStatus.(*AgentRuntimeStatus)
	if !ok {
		t.Fatalf("unexpected get_agent_status result: %#v", rawAgentStatus)
	}
	if agentStatus.Name != BuiltInConciergeAgentName || !agentStatus.BuiltIn {
		t.Fatalf("unexpected concierge status: %+v", agentStatus)
	}

	rawTask, err := svc.toolRegistry.Call(context.Background(), "submit_squad_task", map[string]interface{}{
		"squad_name":  defaultSquadName,
		"prompt":      "prepare a short acknowledgement",
		"agent_names": []interface{}{BuiltInCaptainAgentName},
	})
	if err != nil {
		t.Fatalf("submit_squad_task failed: %v", err)
	}
	taskInfo, ok := rawTask.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected submit_squad_task result: %#v", rawTask)
	}
	if taskInfo["squad_name"] != defaultSquadName {
		t.Fatalf("unexpected squad task result: %#v", taskInfo)
	}
	if taskInfo["status"] != AsyncTaskStatusQueued {
		t.Fatalf("expected queued task, got %#v", taskInfo["status"])
	}

	tasks := manager.ListSharedTasksForSquad(defaultSquadID, time.Time{}, 10)
	if len(tasks) != 1 {
		t.Fatalf("expected one queued squad task, got %+v", tasks)
	}
	if tasks[0].Prompt != "prepare a short acknowledgement" {
		t.Fatalf("unexpected queued prompt: %+v", tasks[0])
	}
}

func TestRegisterCaptainToolsPrefersTeamAsyncAndRemovesSubAgentDelegation(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	svc := &Service{
		agent:        NewAgentWithConfig("Captain", "captain", nil),
		toolRegistry: NewToolRegistry(),
	}
	svc.SetSessionID("captain-session-1")
	svc.toolRegistry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "delegate_to_subagent",
			Description: "delegation helper",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}, nil, CategoryCustom)
	svc.agent.AddTool("delegate_to_subagent", "delegation helper", map[string]interface{}{"type": "object"}, nil)

	manager.RegisterCaptainTools(svc)
	configureCaptainService(svc)

	if svc.toolRegistry.Has("delegate_to_subagent") {
		t.Fatal("expected delegate_to_subagent to be removed from captain tool registry")
	}
	if svc.agent.HasTool("delegate_to_subagent") {
		t.Fatal("expected delegate_to_subagent to be removed from captain agent tools")
	}
	if !svc.agent.HasTool("submit_team_async") {
		t.Fatal("expected submit_team_async to be available on captain")
	}
	if !svc.agent.HasTool("list_team_tasks") {
		t.Fatal("expected list_team_tasks to be available on captain")
	}
	if !svc.agent.HasTool("get_task_status") {
		t.Fatal("expected get_task_status to be available on captain")
	}
	if !svc.agent.HasTool("delegate_task") {
		t.Fatal("expected synchronous delegate_task to remain available on captain")
	}
}

func TestSquadHelpersExposeSquadStyleAPI(t *testing.T) {
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
		Description: "Handles documentation tasks.",
	})
	if err != nil {
		t.Fatalf("create squad failed: %v", err)
	}

	loaded, err := manager.GetSquadByName("docs squad")
	if err != nil {
		t.Fatalf("get squad by name failed: %v", err)
	}
	if loaded.ID != squad.ID {
		t.Fatalf("expected same squad ID, got %s want %s", loaded.ID, squad.ID)
	}

	if _, err := manager.AddSpecialist(context.Background(), squad.ID, "Writer", "Writes docs.", "Write docs."); err != nil {
		t.Fatalf("add specialist failed: %v", err)
	}

	captains, err := manager.ListCaptains()
	if err != nil {
		t.Fatalf("list captains failed: %v", err)
	}
	specialists, err := manager.ListSpecialists()
	if err != nil {
		t.Fatalf("list specialists failed: %v", err)
	}
	if len(captains) < 2 {
		t.Fatalf("expected at least 2 captains, got %d", len(captains))
	}
	if len(specialists) < 1 {
		t.Fatalf("expected at least 1 specialist, got %d", len(specialists))
	}
}

func TestConversationSessionIDIsStablePerConversationAndMember(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)

	first := manager.conversationSessionID("squad-chat-1", "Captain")
	second := manager.conversationSessionID("squad-chat-1", "Captain")
	otherMember := manager.conversationSessionID("squad-chat-1", "Writer")
	otherConversation := manager.conversationSessionID("squad-chat-2", "Captain")

	if first == "" {
		t.Fatal("expected non-empty session id")
	}
	if first != second {
		t.Fatalf("expected stable session id, got %q and %q", first, second)
	}
	if first == otherMember {
		t.Fatal("expected different members to get different session ids")
	}
	if first == otherConversation {
		t.Fatal("expected different conversations to get different session ids")
	}
}

func TestEnqueueSharedTaskQueuesImmediately(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	first, err := manager.EnqueueSharedTask(context.Background(), "Captain", []string{"Captain"}, "first task")
	if err != nil {
		t.Fatalf("enqueue first failed: %v", err)
	}
	second, err := manager.EnqueueSharedTask(context.Background(), "Captain", []string{"Captain"}, "second task")
	if err != nil {
		t.Fatalf("enqueue second failed: %v", err)
	}

	if first.Status != SharedTaskStatusQueued && first.Status != SharedTaskStatusRunning {
		t.Fatalf("expected first task queued or running, got %q", first.Status)
	}
	if second.QueuedAhead < 1 {
		t.Fatalf("expected second task to see queue ahead, got %d", second.QueuedAhead)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tasks := manager.ListSharedTasks("Captain", time.Time{}, 10)
		var completed int
		for _, task := range tasks {
			if task.Status == SharedTaskStatusCompleted || task.Status == SharedTaskStatusFailed {
				completed++
			}
		}
		if completed >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	tasks := manager.ListSharedTasks("Captain", time.Time{}, 10)
	t.Fatalf("expected queued tasks to complete, got %+v", tasks)
}

func TestBuildTeamMemberPrompt_IsAgentSpecific(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo-test"}
	t.Setenv("SHELL", "/bin/test-shell")

	coderPrompt := buildTeamMemberPrompt(&AgentModel{
		Name:         "Coder",
		Instructions: "coder base",
	})
	if !strings.Contains(coderPrompt, "MUST call a filesystem write or modify tool") {
		t.Fatalf("coder prompt missing file-writing rule: %s", coderPrompt)
	}
	if !strings.Contains(coderPrompt, "Do not invent helper tools that are not present in the visible tool list") {
		t.Fatalf("coder prompt missing visible-tool rule: %s", coderPrompt)
	}
	if !strings.Contains(coderPrompt, "fall back to individual read_file calls") {
		t.Fatalf("coder prompt missing multi-read fallback rule: %s", coderPrompt)
	}
	if strings.Contains(coderPrompt, "Never recursively inspect large dependency or build directories") {
		t.Fatalf("coder prompt should not include researcher repo-analysis rules: %s", coderPrompt)
	}
	if strings.Contains(coderPrompt, "Shared writable workspace") {
		t.Fatalf("coder member prompt should not include squad workspace context: %s", coderPrompt)
	}

	captainPrompt := buildTeamMemberPrompt(&AgentModel{
		Name:         "Captain",
		Instructions: "captain base",
	})
	if !strings.Contains(captainPrompt, "Delegate specialized implementation work to specialists") {
		t.Fatalf("captain prompt missing coordinator guidance: %s", captainPrompt)
	}
	if !strings.Contains(captainPrompt, "Do not invent helper tools that are not present in the visible tool list") {
		t.Fatalf("captain prompt missing visible-tool rule: %s", captainPrompt)
	}
	if !strings.Contains(captainPrompt, "Never inspect blacklisted repository paths unless the user explicitly asks for them.") {
		t.Fatalf("captain prompt missing repo-analysis rule: %s", captainPrompt)
	}
	if !strings.Contains(captainPrompt, "read those files first before calling list_directory") {
		t.Fatalf("captain prompt missing direct-file-read rule: %s", captainPrompt)
	}
	if !strings.Contains(captainPrompt, "fall back to individual read_file calls") {
		t.Fatalf("captain prompt missing multi-read fallback rule: %s", captainPrompt)
	}
	if strings.Contains(captainPrompt, "MUST call a filesystem write or modify tool") {
		t.Fatalf("captain prompt should not include coder file-writing rules: %s", captainPrompt)
	}

	teamPrompt := buildTeamSystemPrompt(cfg, &AgentModel{Name: "Captain", Squads: []SquadMembership{{SquadID: defaultSquadID, Role: AgentKindCaptain}}})
	if !strings.Contains(teamPrompt, "Shared writable workspace") {
		t.Fatalf("squad prompt missing workspace context: %s", teamPrompt)
	}
	if !strings.Contains(teamPrompt, "Active project root") {
		t.Fatalf("squad prompt missing project root context: %s", teamPrompt)
	}
	if !strings.Contains(teamPrompt, "Runtime OS/arch: "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("squad prompt missing runtime context: %s", teamPrompt)
	}
	if !strings.Contains(teamPrompt, "Interactive shell: /bin/test-shell") {
		t.Fatalf("squad prompt missing shell context: %s", teamPrompt)
	}
	if !strings.Contains(teamPrompt, "captain for this squad") {
		t.Fatalf("squad prompt missing captain guidance: %s", teamPrompt)
	}
}

func TestBuildTeamSystemPromptForCaptainIncludesRosterAndResponsibilities(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	squad, err := manager.CreateSquad(context.Background(), &Squad{
		Name:        "Delivery Squad",
		Description: "Delivery squad.",
	})
	if err != nil {
		t.Fatalf("create squad failed: %v", err)
	}

	captain, err := manager.GetLeadAgentForSquad(squad.ID)
	if err != nil {
		t.Fatalf("get lead failed: %v", err)
	}
	updatedCaptain, err := manager.UpdateAgent(context.Background(), &AgentModel{
		ID:           captain.ID,
		Name:         captain.Name,
		Description:  "Coordinate the squad and own delivery decisions.",
		Instructions: "Break work into specialist tracks, assign owners, and integrate outputs.",
	})
	if err != nil {
		t.Fatalf("update captain failed: %v", err)
	}
	captain, err = manager.GetMemberByNameInSquad(updatedCaptain.Name, squad.ID)
	if err != nil {
		t.Fatalf("reload captain failed: %v", err)
	}

	if _, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "Delivery Frontend",
		TeamID:       squad.ID,
		Kind:         AgentKindSpecialist,
		Description:  "Owns the web UI implementation.",
		Instructions: "Build the browser UI and keep interaction flows simple.",
	}); err != nil {
		t.Fatalf("create frontend specialist failed: %v", err)
	}
	if _, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "Delivery Backend",
		TeamID:       squad.ID,
		Kind:         AgentKindSpecialist,
		Description:  "Owns the API and persistence layer.",
		Instructions: "Implement APIs, persistence, and server-side integration details.",
		Skills:       []string{"api_design", "storage"},
	}); err != nil {
		t.Fatalf("create backend specialist failed: %v", err)
	}

	prompt := manager.buildTeamSystemPromptForModel(&config.Config{Home: t.TempDir()}, captain)
	if !strings.Contains(prompt, "Captain responsibilities and squad roster:") {
		t.Fatalf("expected captain roster header, got %s", prompt)
	}
	if !strings.Contains(prompt, "Your responsibility summary: Coordinate the squad and own delivery decisions.") {
		t.Fatalf("expected captain responsibility summary, got %s", prompt)
	}
	if !strings.Contains(prompt, "Your operating responsibilities: Break work into specialist tracks, assign owners, and integrate outputs.") {
		t.Fatalf("expected captain operating responsibilities, got %s", prompt)
	}
	if !strings.Contains(prompt, "Delivery Frontend [specialist]: Owns the web UI implementation.") {
		t.Fatalf("expected frontend member roster entry, got %s", prompt)
	}
	if !strings.Contains(prompt, "Responsibilities: Build the browser UI and keep interaction flows simple.") {
		t.Fatalf("expected frontend responsibilities, got %s", prompt)
	}
	if !strings.Contains(prompt, "Delivery Backend [specialist]: Owns the API and persistence layer.") {
		t.Fatalf("expected backend member roster entry, got %s", prompt)
	}
	if !strings.Contains(prompt, "Skills: api_design, storage") {
		t.Fatalf("expected backend skills in roster, got %s", prompt)
	}
}

func TestCreateMemberClearsCaptainServiceCache(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	squad, err := manager.CreateSquad(context.Background(), &Squad{
		Name:        "Cache Squad",
		Description: "Cache squad.",
	})
	if err != nil {
		t.Fatalf("create squad failed: %v", err)
	}
	captain, err := manager.GetLeadAgentForSquad(squad.ID)
	if err != nil {
		t.Fatalf("get lead failed: %v", err)
	}

	manager.mu.Lock()
	manager.services[captain.Name] = &Service{}
	manager.mu.Unlock()

	if _, err := manager.CreateMember(context.Background(), &AgentModel{
		Name:         "Cache Specialist",
		TeamID:       squad.ID,
		Kind:         AgentKindSpecialist,
		Description:  "Handles specialist work.",
		Instructions: "Do specialist work.",
	}); err != nil {
		t.Fatalf("create specialist failed: %v", err)
	}

	manager.mu.RLock()
	_, exists := manager.services[captain.Name]
	manager.mu.RUnlock()
	if exists {
		t.Fatal("expected captain service cache to be cleared after squad membership change")
	}
}

func TestPersistedSharedTasksRestoreAcrossManagerInstances(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewSquadManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	task := &SharedTask{
		ID:          "shared-task-1",
		SessionID:   "session-restore-1",
		SquadID:     defaultSquadID,
		SquadName:   defaultSquadName,
		CaptainName: BuiltInCaptainAgentName,
		AgentNames:  []string{BuiltInCaptainAgentName},
		Prompt:      "review persisted squad work",
		AckMessage:  "Captain received that. Starting it now.",
		Status:      SharedTaskStatusCompleted,
		ResultText:  "done",
		CreatedAt:   time.Now().Add(-time.Minute),
	}
	if err := store.SaveSharedTask(task); err != nil {
		t.Fatalf("save shared task failed: %v", err)
	}

	restored := NewSquadManager(store)
	tasks := restored.ListSharedTasksForSquad(defaultSquadID, time.Time{}, 10)
	if len(tasks) != 1 {
		t.Fatalf("expected restored shared task, got %+v", tasks)
	}
	if tasks[0].ID != task.ID || tasks[0].ResultText != "done" {
		t.Fatalf("unexpected restored shared task: %+v", tasks[0])
	}

	asyncTask, err := restored.GetTask(task.ID)
	if err != nil {
		t.Fatalf("expected shared task to be visible via GetTask after restore: %v", err)
	}
	if asyncTask.SessionID != task.SessionID {
		t.Fatalf("expected restored session id %q, got %q", task.SessionID, asyncTask.SessionID)
	}
	if asyncTask.Status != AsyncTaskStatusCompleted {
		t.Fatalf("expected completed async status, got %q", asyncTask.Status)
	}
}

func TestDefaultMemberMCPTools_CaptainIncludesFilesystemAndWebTools(t *testing.T) {
	tools := defaultMemberMCPTools("Captain")
	if len(tools) == 0 {
		t.Fatal("expected captain default tool allowlist")
	}
	if containsStr(tools, "mcp_filesystem_tree") {
		t.Fatalf("captain should not have tree by default: %v", tools)
	}
	if containsStr(tools, "mcp_filesystem_search_files") {
		t.Fatalf("captain should not have broad file search by default: %v", tools)
	}
	if !containsStr(tools, "mcp_filesystem_read_file") {
		t.Fatalf("captain should keep targeted file reads: %v", tools)
	}
	if !containsStr(tools, "mcp_filesystem_write_file") {
		t.Fatalf("captain should have file write capability: %v", tools)
	}
	if !containsStr(tools, "mcp_websearch_websearch_ai_summary") {
		t.Fatalf("captain should have websearch capability: %v", tools)
	}
}

func TestBuildTeamTaskEnvelope_ContainsSharedContext(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo-test"}
	t.Setenv("SHELL", "/bin/test-shell")

	envelope := buildTeamTaskEnvelope(cfg, "Assistant", "summarize the repo flow")
	if !strings.Contains(envelope, "Squad task context:") {
		t.Fatalf("missing squad task header: %s", envelope)
	}
	if !strings.Contains(envelope, "Target squad agent: Assistant") {
		t.Fatalf("missing target squad agent: %s", envelope)
	}
	if !strings.Contains(envelope, "Shared writable workspace: /tmp/agentgo-test/workspace") {
		t.Fatalf("missing workspace context: %s", envelope)
	}
	if !strings.Contains(envelope, "Active project root:") {
		t.Fatalf("missing project root context: %s", envelope)
	}
	if !strings.Contains(envelope, "Runtime OS/arch: "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("missing runtime context: %s", envelope)
	}
	if !strings.Contains(envelope, "Interactive shell: /bin/test-shell") {
		t.Fatalf("missing shell context: %s", envelope)
	}
	if !strings.Contains(envelope, "The bullets above are context, not the requested action.") {
		t.Fatalf("missing context-vs-task guardrail: %s", envelope)
	}
	if !strings.Contains(envelope, "Ignore blacklisted repository paths unless the user explicitly asks for them:") {
		t.Fatalf("missing blacklist guardrail: %s", envelope)
	}
	if !strings.Contains(envelope, "\nTask:\nsummarize the repo flow") {
		t.Fatalf("missing original task body: %s", envelope)
	}
}

func TestFilterToolDefinitionsForAgent_RespectsAllowlist(t *testing.T) {
	svc := &Service{}
	currentAgent := NewAgentWithConfig("Assistant", "assistant", nil)
	currentAgent.SetAllowedMCPTools([]string{
		"mcp_filesystem_read_file",
		"mcp_filesystem_list_directory",
	})
	currentAgent.SetAllowedSkills([]string{"repo_map"})

	defs := []domain.ToolDefinition{
		{Function: domain.ToolFunction{Name: "mcp_filesystem_read_file"}},
		{Function: domain.ToolFunction{Name: "mcp_filesystem_tree"}},
		{Function: domain.ToolFunction{Name: "skill_repo_map"}},
		{Function: domain.ToolFunction{Name: "skill_shell"}},
		{Function: domain.ToolFunction{Name: "search_available_tools"}},
	}

	filtered := svc.filterToolDefinitionsForAgent(currentAgent, defs)
	names := make([]string, 0, len(filtered))
	for _, def := range filtered {
		names = append(names, def.Function.Name)
	}

	if !containsStr(names, "mcp_filesystem_read_file") {
		t.Fatalf("expected allowed mcp tool to remain: %v", names)
	}
	if containsStr(names, "mcp_filesystem_tree") {
		t.Fatalf("expected disallowed mcp tool to be filtered: %v", names)
	}
	if !containsStr(names, "skill_repo_map") {
		t.Fatalf("expected allowed skill to remain: %v", names)
	}
	if containsStr(names, "skill_shell") {
		t.Fatalf("expected disallowed skill to be filtered: %v", names)
	}
	if !containsStr(names, "search_available_tools") {
		t.Fatalf("expected search_available_tools to remain: %v", names)
	}
}

func TestIsBlacklistedRepositoryPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "/repo/ui/node_modules/react/index.js", want: true},
		{path: "/repo/rust/target/debug/app", want: true},
		{path: "/repo/.git/config", want: true},
		{path: "/repo/pkg/agent/team_manager.go", want: false},
		{path: "/repo/ui/src/App.tsx", want: false},
	}

	for _, tc := range cases {
		if got := IsBlacklistedRepositoryPath(tc.path); got != tc.want {
			t.Fatalf("IsBlacklistedRepositoryPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestDispatchRunOptions(t *testing.T) {
	coder := dispatchRunOptions("Coder")
	if len(coder) != 2 {
		t.Fatalf("expected coder-specific options, got %d", len(coder))
	}

	assistant := dispatchRunOptions("Assistant")
	if len(assistant) != 2 {
		t.Fatalf("expected tuned dispatch options for assistant, got %d", len(assistant))
	}
}
