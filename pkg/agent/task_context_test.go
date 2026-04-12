package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestRunOptionWithTaskIDSetsConfig(t *testing.T) {
	cfg := DefaultRunConfig()
	WithTaskID("task-123")(cfg)
	if cfg.TaskID != "task-123" {
		t.Fatalf("expected task id to be set, got %q", cfg.TaskID)
	}
}

func TestRunOptionWithPTCEnabledCanDisablePTCForRun(t *testing.T) {
	cfg := DefaultRunConfig()
	WithPTCEnabled(false)(cfg)
	if !cfg.DisablePTC {
		t.Fatal("expected PTC to be disabled for this run")
	}
	WithPTCEnabled(true)(cfg)
	if cfg.DisablePTC {
		t.Fatal("expected PTC to be enabled for this run")
	}
}

func TestPrepareDispatchRequestIncludesTaskID(t *testing.T) {
	manager := &TeamManager{}
	_, opts := manager.prepareDispatchRequest("Assistant", "hello", "session-1", "task-42", nil)

	cfg := DefaultRunConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.SessionID != "session-1" {
		t.Fatalf("expected session id, got %q", cfg.SessionID)
	}
	if cfg.TaskID != "task-42" {
		t.Fatalf("expected task id, got %q", cfg.TaskID)
	}
}

func TestEnsureTaskIDReusesSessionTaskID(t *testing.T) {
	session := NewSession("agent-1")
	session.SetContext(sessionContextTaskID, "task-existing")
	cfg := DefaultRunConfig()

	got := ensureTaskID(session, cfg)
	if got != "task-existing" {
		t.Fatalf("expected existing task id, got %q", got)
	}
	if cfg.TaskID != "task-existing" {
		t.Fatalf("expected config task id, got %q", cfg.TaskID)
	}
}

func TestHistoryForTaskFiltersByTaskID(t *testing.T) {
	history := []domain.Message{
		{Role: "user", Content: "a", TaskID: "task-a"},
		{Role: "assistant", Content: "b", TaskID: "task-a"},
		{Role: "user", Content: "c", TaskID: "task-b"},
	}
	filtered := historyForTask(history, "task-a")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 task messages, got %d", len(filtered))
	}
	for _, msg := range filtered {
		if msg.TaskID != "task-a" {
			t.Fatalf("unexpected task id in filtered history: %+v", msg)
		}
	}
}

func TestAsyncTaskDefaultsTaskID(t *testing.T) {
	manager := newTaskTestManager()
	task := &AsyncTask{
		ID:        "task-live",
		SessionID: "session-1",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusQueued,
		AgentName: "Assistant",
		CreatedAt: time.Now(),
	}
	manager.upsertAsyncTask(task)
	got, err := manager.GetTask("task-live")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.TaskID != "" {
		t.Fatalf("expected no implicit task id mutation on generic upsert, got %q", got.TaskID)
	}
}

func TestUnifiedTaskHydratesMessagesForTask(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := NewTeamManager(store)

	task := &AsyncTask{
		ID:        "async-1",
		TaskID:    "task-1",
		SessionID: "creator-session",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusCompleted,
		AgentName: "Assistant",
		Prompt:    "do work",
		CreatedAt: time.Now(),
	}
	manager.upsertAsyncTask(task)

	session := NewSession("Assistant")
	session.ID = "runtime-session"
	session.AddMessage(domain.Message{Role: "user", Content: "first turn", TaskID: "task-1"})
	session.AddMessage(domain.Message{Role: "assistant", Content: "tool call", TaskID: "task-1", ToolCalls: []domain.ToolCall{{
		ID: "call_1",
		Function: domain.FunctionCall{
			Name:      "memory_recall",
			Arguments: map[string]interface{}{"query": "x"},
		},
	}}})
	session.AddMessage(domain.Message{Role: "tool", Content: "tool result", TaskID: "task-1", ToolCallID: "call_1"})
	session.AddMessage(domain.Message{Role: "user", Content: "other task", TaskID: "task-2"})
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	unified, err := manager.GetUnifiedTask("async-1")
	if err != nil {
		t.Fatalf("GetUnifiedTask() error = %v", err)
	}
	if len(unified.Frames) != 3 {
		t.Fatalf("expected 3 task frames, got %d", len(unified.Frames))
	}
	for _, message := range unified.Frames {
		if message.SessionID != "runtime-session" {
			t.Fatalf("expected runtime session id, got %q", message.SessionID)
		}
		if message.Message.TaskID != "task-1" {
			t.Fatalf("unexpected hydrated task id: %+v", message.Message)
		}
	}
}

func TestTaskYieldResumePersistsFutureState(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := NewTeamManager(store)
	task := &AsyncTask{
		ID:        "future-1",
		TaskID:    "future-1",
		SessionID: "session-future",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusRunning,
		AgentName: "Assistant",
		Prompt:    "wait for input",
		CreatedAt: time.Now(),
	}
	manager.upsertAsyncTask(task)

	yielded, err := manager.YieldTask(t.Context(), "future-1", "waiting for user input")
	if err != nil {
		t.Fatalf("YieldTask() error = %v", err)
	}
	if yielded.Status != "yielded" {
		t.Fatalf("expected yielded status, got %q", yielded.Status)
	}
	if yielded.Awaiting == nil || yielded.Awaiting.Reason != "waiting for user input" {
		t.Fatalf("expected awaiting reason, got %+v", yielded.Awaiting)
	}

	restored, err := store.GetTask("future-1")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if restored.Status != "yielded" {
		t.Fatalf("persisted status = %q, want yielded", restored.Status)
	}
	if restored.Awaiting == nil || restored.Awaiting.Type != "resume" {
		t.Fatalf("persisted awaiting = %+v, want resume state", restored.Awaiting)
	}

	resumed, err := manager.ResumeTask(t.Context(), "future-1", "continue")
	if err != nil {
		t.Fatalf("ResumeTask() error = %v", err)
	}
	if resumed.Status != "resuming" {
		t.Fatalf("expected resuming status, got %q", resumed.Status)
	}
}

func TestTaskServiceFacade(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := NewTeamManager(store)
	manager.upsertAsyncTask(&AsyncTask{
		ID:        "task-service-1",
		TaskID:    "task-service-1",
		SessionID: "session-service",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusRunning,
		AgentName: "Assistant",
		Prompt:    "do work",
		CreatedAt: time.Now(),
	})

	tasks := manager.Tasks()
	list, err := tasks.List(t.Context(), TaskListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != "task-service-1" {
		t.Fatalf("unexpected list: %+v", list)
	}

	yielded, err := tasks.Yield(t.Context(), "task-service-1", TaskYieldOptions{Reason: "pause"})
	if err != nil {
		t.Fatalf("Yield() error = %v", err)
	}
	if yielded.Status != "yielded" {
		t.Fatalf("expected yielded, got %q", yielded.Status)
	}

	resumed, err := tasks.Resume(t.Context(), "task-service-1", TaskResumeOptions{Input: "continue"})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Status != "resuming" {
		t.Fatalf("expected resuming, got %q", resumed.Status)
	}

	cancelled, err := tasks.Cancel(t.Context(), "task-service-1")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %q", cancelled.Status)
	}
}

func TestTaskServiceSubmitReturnsCanonicalTask(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agentgo.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	manager := NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("SeedDefaultMembers() error = %v", err)
	}

	submitted, err := manager.Tasks().Submit(t.Context(), TaskSubmitOptions{
		SessionID: "session-submit",
		AgentName: defaultAssistantAgentName,
		Input:     "say hello",
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if submitted.ID == "" || submitted.Kind != "agent" || submitted.Input != "say hello" {
		t.Fatalf("unexpected submitted task: %+v", submitted)
	}
}
