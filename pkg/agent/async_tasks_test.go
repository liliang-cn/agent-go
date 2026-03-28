package agent

import (
	"context"
	"testing"
	"time"
)

func newTaskTestManager() *TeamManager {
	return &TeamManager{
		asyncTasks:   make(map[string]*AsyncTask),
		sessionTasks: make(map[string][]string),
		taskSubs:     make(map[string]map[chan *TaskEvent]struct{}),
		taskCancels:  make(map[string]context.CancelFunc),
		teamRequests: make(map[string]*TeamRequest),
	}
}

func TestSubscribeTaskReplaysBacklogForTerminalTask(t *testing.T) {
	manager := newTaskTestManager()
	task := &AsyncTask{
		ID:        "task-terminal",
		SessionID: "session-1",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusQueued,
		AgentName: "Assistant",
		CreatedAt: time.Now(),
	}
	manager.upsertAsyncTask(task)
	manager.emitTaskEvent(task.ID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Type:      TaskEventTypeCreated,
		AgentName: task.AgentName,
		Timestamp: task.CreatedAt,
	}, false)
	manager.completeAsyncTask(task.ID, "done", "Assistant")

	events, _, err := manager.SubscribeTask(task.ID)
	if err != nil {
		t.Fatalf("SubscribeTask() error = %v", err)
	}

	var got []TaskEventType
	for evt := range events {
		got = append(got, evt.Type)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 replayed events, got %d", len(got))
	}
	if got[0] != TaskEventTypeCreated || got[1] != TaskEventTypeCompleted {
		t.Fatalf("unexpected event sequence: %v", got)
	}
}

func TestSubscribeTaskReceivesLiveRuntimeEvent(t *testing.T) {
	manager := newTaskTestManager()
	task := &AsyncTask{
		ID:        "task-live",
		SessionID: "session-2",
		Kind:      AsyncTaskKindTeam,
		Status:    AsyncTaskStatusRunning,
		TeamID:    "team-1",
		TeamName:  "AgentGo Team",
		CreatedAt: time.Now(),
	}
	manager.upsertAsyncTask(task)

	events, unsubscribe, err := manager.SubscribeTask(task.ID)
	if err != nil {
		t.Fatalf("SubscribeTask() error = %v", err)
	}
	defer unsubscribe()

	manager.emitTaskEvent(task.ID, &TaskEvent{
		TaskID:    task.ID,
		Type:      TaskEventTypeRuntime,
		AgentName: "Captain",
		Runtime: &Event{
			Type:      EventTypeToolCall,
			AgentName: "Captain",
			ToolName:  "mcp_filesystem_read_file",
			Timestamp: time.Now(),
		},
		Timestamp: time.Now(),
	}, false)

	select {
	case evt := <-events:
		if evt.Type != TaskEventTypeRuntime {
			t.Fatalf("expected runtime event, got %s", evt.Type)
		}
		if evt.Runtime == nil || evt.Runtime.ToolName != "mcp_filesystem_read_file" {
			t.Fatalf("unexpected runtime payload: %#v", evt.Runtime)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime event")
	}
}

func TestEnsureAsyncTaskForSharedTaskIndexesSession(t *testing.T) {
	manager := newTaskTestManager()
	shared := &SharedTask{
		ID:          "shared-1",
		TeamID:      "team-1",
		CaptainName: "Captain",
		AgentNames:  []string{"Captain"},
		Prompt:      "hello",
		Status:      SharedTaskStatusQueued,
		AckMessage:  "Captain received that.",
		CreatedAt:   time.Now(),
	}

	task := manager.ensureAsyncTaskForSharedTask(shared, "session-3", "AgentGo Team")
	if task == nil {
		t.Fatal("expected async task")
	}
	if task.Kind != AsyncTaskKindTeam {
		t.Fatalf("expected team task, got %s", task.Kind)
	}
	if task.SessionID != "session-3" {
		t.Fatalf("expected session to be indexed, got %q", task.SessionID)
	}

	sessionTasks := manager.ListSessionTasks("session-3", 10)
	if len(sessionTasks) != 1 || sessionTasks[0].ID != shared.ID {
		t.Fatalf("unexpected session tasks: %#v", sessionTasks)
	}
}
