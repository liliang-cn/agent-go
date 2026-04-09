package agent

import (
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
