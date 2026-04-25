package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
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
		AgentName: "Responder",
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
	manager.completeAsyncTask(task.ID, "done", "Responder")

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

func TestSubscribeTaskReplaysBlockedTerminalTask(t *testing.T) {
	manager := newTaskTestManager()
	task := &AsyncTask{
		ID:        "task-blocked",
		SessionID: "session-blocked",
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusQueued,
		AgentName: "Operator",
		CreatedAt: time.Now(),
	}
	manager.upsertAsyncTask(task)
	manager.blockAsyncTask(task.ID, "missing permission", "Operator")

	events, _, err := manager.SubscribeTask(task.ID)
	if err != nil {
		t.Fatalf("SubscribeTask() error = %v", err)
	}
	var got []TaskEventType
	for evt := range events {
		got = append(got, evt.Type)
	}

	if len(got) != 1 || got[0] != TaskEventTypeBlocked {
		t.Fatalf("unexpected replayed events: %v", got)
	}
	unified, err := manager.GetUnifiedTask(task.ID)
	if err != nil {
		t.Fatalf("GetUnifiedTask() error = %v", err)
	}
	if unified.Status != "blocked" || unified.Error != "missing permission" {
		t.Fatalf("expected blocked canonical task, got %+v", unified)
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
		AgentName: "Orchestrator",
		Runtime: &Event{
			Type:      EventTypeToolCall,
			AgentName: "Orchestrator",
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
		ID:               "shared-1",
		TeamID:           "team-1",
		OrchestratorName: "Orchestrator",
		AgentNames:       []string{"Orchestrator"},
		Prompt:           "hello",
		Status:           SharedTaskStatusQueued,
		AckMessage:       "Orchestrator received that.",
		CreatedAt:        time.Now(),
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

func TestExecuteSharedTaskStreamCreatesChildTasksPerAgent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	manager.builtInStreamDispatchOverride = func(ctx context.Context, agentName, instruction string, runOptions []RunOption) (<-chan *Event, error) {
		session := NewSession(uuid.NewString())
		cfg := DefaultRunConfig()
		for _, opt := range runOptions {
			opt(cfg)
		}
		if cfg.SessionID != "" {
			session.ID = cfg.SessionID
		}
		taskID := cfg.TaskID
		events := make(chan *Event, 2)
		go func() {
			defer close(events)
			session.AddMessage(domain.Message{Role: "user", Content: instruction, TaskID: taskID})
			_ = store.SaveSession(session)
			session.AddMessage(domain.Message{Role: "assistant", Content: "done by " + agentName, TaskID: taskID})
			_ = store.SaveSession(session)
			events <- &Event{Type: EventTypeComplete, AgentName: agentName, Content: "done by " + agentName, Timestamp: time.Now()}
		}()
		return events, nil
	}

	shared := &SharedTask{
		ID:               "shared-root",
		SessionID:        "team-session",
		TeamID:           "team-1",
		TeamName:         "AgentGo Team",
		OrchestratorName: "Orchestrator",
		AgentNames:       []string{"Responder", "Operator"},
		Prompt:           "do shared work",
		Status:           SharedTaskStatusQueued,
		CreatedAt:        time.Now(),
	}

	manager.executeSharedTaskStream(context.Background(), shared)

	tasks, err := store.ListTasks(20)
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	var childCount int
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if task.ParentTaskID == "shared-root" {
			childCount++
		}
	}
	if childCount != 2 {
		t.Fatalf("expected 2 child tasks for shared team task, got %d", childCount)
	}
}
