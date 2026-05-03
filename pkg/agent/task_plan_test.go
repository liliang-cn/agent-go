package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTaskPlanCreateNormalizesDependenciesAndReadyItems(t *testing.T) {
	manager := NewTeamManager(newTaskPlanTestStore(t))

	plan, err := manager.Plans().Create(context.Background(), TaskPlanCreateOptions{
		SessionID: "session-plan",
		Goal:      "ship the feature",
		Items: []TaskPlanItem{
			{ID: "inspect", Subject: "Inspect current implementation", Blocks: []string{"implement"}},
			{ID: "implement", Subject: "Implement the change"},
			{ID: "verify", Subject: "Verify behavior", BlockedBy: []string{"implement"}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if plan.ID == "" || plan.SessionID != "session-plan" {
		t.Fatalf("unexpected plan identity: %+v", plan)
	}
	if got := plan.Items[1].BlockedBy; len(got) != 1 || got[0] != "inspect" {
		t.Fatalf("expected implement to be blocked by inspect, got %+v", got)
	}
	if got := plan.Items[1].Blocks; len(got) != 1 || got[0] != "verify" {
		t.Fatalf("expected implement to block verify, got %+v", got)
	}

	ready, err := manager.Plans().ReadyItems(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("ReadyItems() error = %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "inspect" {
		t.Fatalf("expected only inspect to be ready, got %+v", ready)
	}
}

func TestTaskPlanUpdateItemUnblocksDependents(t *testing.T) {
	manager := NewTeamManager(newTaskPlanTestStore(t))
	plan, err := manager.Plans().Create(context.Background(), TaskPlanCreateOptions{
		Goal: "ship the feature",
		Items: []TaskPlanItem{
			{ID: "inspect", Subject: "Inspect current implementation", Blocks: []string{"implement"}},
			{ID: "implement", Subject: "Implement the change"},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	status := PlanItemStatusCompleted
	if _, err := manager.Plans().UpdateItem(context.Background(), plan.ID, "inspect", TaskPlanItemUpdateOptions{Status: &status}); err != nil {
		t.Fatalf("UpdateItem() error = %v", err)
	}

	ready, err := manager.Plans().ReadyItems(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("ReadyItems() error = %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "implement" {
		t.Fatalf("expected implement to be ready after inspect completed, got %+v", ready)
	}
}

func TestTaskPlanSubmitItemLinksExecutionTask(t *testing.T) {
	manager := NewTeamManager(newTaskPlanTestStore(t))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("SeedDefaultMembers() error = %v", err)
	}

	plan, err := manager.Plans().Create(context.Background(), TaskPlanCreateOptions{
		SessionID: "session-plan",
		Goal:      "answer simply",
		Items: []TaskPlanItem{
			{ID: "respond", Subject: "Respond to the user", OwnerAgent: defaultResponderAgentName},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	task, err := manager.Plans().SubmitItem(context.Background(), plan.ID, "respond", TaskPlanSubmitItemOptions{
		Input: "say hello",
	})
	if err != nil {
		t.Fatalf("SubmitItem() error = %v", err)
	}
	if task.ID == "" || task.Kind != "agent" {
		t.Fatalf("unexpected execution task: %+v", task)
	}

	updated, err := manager.Plans().Get(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	item := updated.Items[0]
	if item.Status != PlanItemStatusInProgress {
		t.Fatalf("expected item to be in progress, got %q", item.Status)
	}
	if item.ExecutionTaskID != task.ID {
		t.Fatalf("expected execution task id %q, got %q", task.ID, item.ExecutionTaskID)
	}
}

func TestTaskPlanExecutionTaskStatusSync(t *testing.T) {
	manager := NewTeamManager(newTaskPlanTestStore(t))
	plan, err := manager.Plans().Create(context.Background(), TaskPlanCreateOptions{
		SessionID: "sync-session",
		Goal:      "sync execution status",
		Items: []TaskPlanItem{
			{ID: "run", Subject: "Run work", OwnerAgent: defaultResponderAgentName},
			{ID: "next", Subject: "Next work", BlockedBy: []string{"run"}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	taskID := "task-sync-1"
	inProgress := PlanItemStatusInProgress
	_, err = manager.Plans().UpdateItem(context.Background(), plan.ID, "run", TaskPlanItemUpdateOptions{
		Status:          &inProgress,
		ExecutionTaskID: &taskID,
	})
	if err != nil {
		t.Fatalf("UpdateItem() error = %v", err)
	}

	manager.updateTaskPlanItemForExecutionTask(taskID, PlanItemStatusCompleted, "done", "")

	updated, err := manager.Plans().Get(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if updated.Items[0].Status != PlanItemStatusCompleted || updated.Items[0].ResultText != "done" {
		t.Fatalf("expected completed item with result, got %+v", updated.Items[0])
	}
	ready, err := manager.Plans().ReadyItems(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("ReadyItems() error = %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "next" {
		t.Fatalf("expected dependent item to become ready, got %+v", ready)
	}
}

func TestTaskPlanPersistsAcrossManagerInstances(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agentgo.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := NewTeamManager(store)

	plan, err := manager.Plans().Create(context.Background(), TaskPlanCreateOptions{
		SessionID: "session-persist",
		Goal:      "persist plan",
		Items: []TaskPlanItem{
			{ID: "one", Subject: "Do one thing"},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	restoredStore, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(restored) error = %v", err)
	}
	restored := NewTeamManager(restoredStore)
	got, err := restored.Plans().Get(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("Get(restored) error = %v", err)
	}
	if got.Goal != "persist plan" || len(got.Items) != 1 || got.Items[0].ID != "one" {
		t.Fatalf("unexpected restored plan: %+v", got)
	}
}

func TestTaskPlanToolsCreateListAndUpdate(t *testing.T) {
	manager := newSeededPipelineManager(t)
	svc, err := manager.GetAgentService(BuiltInOrchestratorAgentName)
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	manager.RegisterOrchestratorTools(svc)

	if !svc.toolRegistry.Has("task_plan_create") || !svc.toolRegistry.Has("task_plan_update") || !svc.toolRegistry.Has("task_plan_list") {
		t.Fatal("expected task plan tools to be registered")
	}

	raw, err := svc.toolRegistry.Call(context.Background(), "task_plan_create", map[string]interface{}{
		"goal": "ship feature",
		"items": []interface{}{
			map[string]interface{}{
				"id":          "inspect",
				"subject":     "Inspect implementation",
				"owner_agent": defaultResponderAgentName,
				"blocks":      []interface{}{"verify"},
			},
			map[string]interface{}{
				"id":      "verify",
				"subject": "Verify behavior",
			},
		},
	})
	if err != nil {
		t.Fatalf("task_plan_create error = %v", err)
	}
	created, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected create result: %#v", raw)
	}
	planID, _ := created["plan_id"].(string)
	if planID == "" {
		t.Fatalf("missing plan id: %#v", created)
	}

	status := "completed"
	updatedRaw, err := svc.toolRegistry.Call(context.Background(), "task_plan_update", map[string]interface{}{
		"plan_id": planID,
		"item_id": "inspect",
		"status":  status,
	})
	if err != nil {
		t.Fatalf("task_plan_update error = %v", err)
	}
	updated, _ := updatedRaw.(map[string]interface{})
	if updated["status"] != PlanItemStatusCompleted {
		t.Fatalf("expected completed status, got %#v", updated)
	}

	listRaw, err := svc.toolRegistry.Call(context.Background(), "task_plan_list", map[string]interface{}{"limit": 10})
	if err != nil {
		t.Fatalf("task_plan_list error = %v", err)
	}
	list, ok := listRaw.([]map[string]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("unexpected list result: %#v", listRaw)
	}
}

func newTaskPlanTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}
