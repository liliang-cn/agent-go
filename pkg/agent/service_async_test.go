package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

type persistedAsyncTestLLM struct {
	mu      sync.Mutex
	results []string
	calls   int
}

func (l *persistedAsyncTestLLM) Generate(context.Context, string, *domain.GenerationOptions) (string, error) {
	return "", nil
}

func (l *persistedAsyncTestLLM) Stream(context.Context, string, *domain.GenerationOptions, func(string)) error {
	return nil
}

func (l *persistedAsyncTestLLM) GenerateWithTools(_ context.Context, _ []domain.Message, _ []domain.ToolDefinition, _ *domain.GenerationOptions) (*domain.GenerationResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.calls >= len(l.results) {
		return nil, fmt.Errorf("unexpected GenerateWithTools call %d", l.calls+1)
	}
	l.calls++
	result := l.results[l.calls-1]
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   fmt.Sprintf("call-%d", l.calls),
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "task_complete",
				Arguments: map[string]interface{}{"result": result},
			},
		}},
	}, nil
}

func (l *persistedAsyncTestLLM) StreamWithTools(ctx context.Context, messages []domain.Message, tools []domain.ToolDefinition, opts *domain.GenerationOptions, callback domain.ToolCallCallback) error {
	res, err := l.GenerateWithTools(ctx, messages, tools, opts)
	if err != nil {
		return err
	}
	return callback(res)
}

func (l *persistedAsyncTestLLM) GenerateStructured(context.Context, string, interface{}, *domain.GenerationOptions) (*domain.StructuredResult, error) {
	return &domain.StructuredResult{
		Valid: true,
		Raw:   `{"intent_type":"analysis","confidence":0.9}`,
	}, nil
}

func (l *persistedAsyncTestLLM) RecognizeIntent(context.Context, string) (*domain.IntentResult, error) {
	return nil, nil
}

func TestPersistedAsyncContinuationSupportsCrossServiceSendMessage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	llm := &persistedAsyncTestLLM{results: []string{"ASYNC_DONE", "RESUME_DONE"}}

	svc1, err := NewService(llm, nil, nil, dbPath, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	defer svc1.Close()

	parent1 := NewSessionWithID("parent-session-1", svc1.agent.ID())
	parent1.SetContext(sessionContextTaskID, "root-task-1")
	if err := svc1.store.SaveSession(parent1); err != nil {
		t.Fatalf("SaveSession(parent1) error = %v", err)
	}

	ctx1 := withCurrentSession(context.Background(), parent1)
	raw, err := svc1.executeDelegateAsync(ctx1, svc1.agent, map[string]interface{}{
		"goal": "first goal",
		"name": "bg-one",
	})
	if err != nil {
		t.Fatalf("executeDelegateAsync() error = %v", err)
	}
	resp, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("delegate_async response = %#v", raw)
	}
	taskID, _ := resp["task_id"].(string)
	if taskID == "" {
		t.Fatalf("delegate_async task_id = %#v", resp["task_id"])
	}

	firstTask := waitForTaskOutput(t, svc1.store, taskID, "ASYNC_DONE")
	if firstTask.Source != persistedAsyncTaskSource {
		t.Fatalf("task.Source = %q, want %q", firstTask.Source, persistedAsyncTaskSource)
	}
	if firstTask.ParentTaskID != "root-task-1" {
		t.Fatalf("task.ParentTaskID = %q, want %q", firstTask.ParentTaskID, "root-task-1")
	}
	if strings.TrimSpace(firstTask.RuntimeSessionID) == "" {
		t.Fatal("expected runtime session id to persist")
	}
	runtimeSessionID := firstTask.RuntimeSessionID

	waitForSessionNotification(t, svc1.store, parent1.ID, "ASYNC_DONE")

	svc2, err := NewService(llm, nil, nil, dbPath, nil)
	if err != nil {
		t.Fatalf("NewService(second) error = %v", err)
	}
	defer svc2.Close()

	parent2 := NewSessionWithID("parent-session-2", svc2.agent.ID())
	if err := svc2.store.SaveSession(parent2); err != nil {
		t.Fatalf("SaveSession(parent2) error = %v", err)
	}

	ctx2 := withCurrentSession(context.Background(), parent2)
	raw, err = svc2.executeSendMessage(ctx2, svc2.agent, map[string]interface{}{
		"to":      taskID,
		"message": "second goal",
	})
	if err != nil {
		t.Fatalf("executeSendMessage() error = %v", err)
	}
	resp, ok = raw.(map[string]interface{})
	if !ok || resp["status"] != "message_sent" {
		t.Fatalf("send_message response = %#v", raw)
	}

	secondTask := waitForTaskOutput(t, svc2.store, taskID, "RESUME_DONE")
	if secondTask.RuntimeSessionID != runtimeSessionID {
		t.Fatalf("runtime session id changed: %q -> %q", runtimeSessionID, secondTask.RuntimeSessionID)
	}
	if secondTask.Status != taskpkg.StatusCompleted {
		t.Fatalf("task.Status = %q, want %q", secondTask.Status, taskpkg.StatusCompleted)
	}

	runtimeSession, err := svc2.store.GetSession(runtimeSessionID)
	if err != nil {
		t.Fatalf("GetSession(runtime) error = %v", err)
	}
	history := taskHistory(runtimeSession, taskID)
	if !containsMessageContent(history, "first goal") {
		t.Fatalf("expected runtime session to retain first goal, got %#v", history)
	}
	if !containsMessageContent(history, "second goal") {
		t.Fatalf("expected runtime session to retain resumed goal, got %#v", history)
	}

	waitForSessionNotification(t, svc2.store, parent2.ID, "RESUME_DONE")
}

// asyncTaskWaitTimeout is generous enough to absorb slow CI runners
// (hosted Linux with cold module cache + SQLite contention can take
// several seconds for a task to flush its result row). 5s was the
// previous value; it flaked on ubuntu-latest. Locally these waits
// resolve in well under a second.
const asyncTaskWaitTimeout = 30 * time.Second

func waitForTaskOutput(t *testing.T, store *Store, taskID, wantOutput string) *taskpkg.Task {
	t.Helper()
	deadline := time.Now().Add(asyncTaskWaitTimeout)
	for time.Now().Before(deadline) {
		task, err := store.GetTask(taskID)
		if err == nil && task != nil && strings.TrimSpace(task.Output) == wantOutput {
			return task
		}
		time.Sleep(25 * time.Millisecond)
	}
	task, _ := store.GetTask(taskID)
	t.Fatalf("task %s did not reach output %q, last task = %+v", taskID, wantOutput, task)
	return nil
}

func waitForSessionNotification(t *testing.T, store *Store, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(asyncTaskWaitTimeout)
	for time.Now().Before(deadline) {
		session, err := store.GetSession(sessionID)
		if err == nil && session != nil {
			for _, msg := range session.GetMessages() {
				if msg.Role == "user" && strings.Contains(msg.Content, "<task-notification>") && strings.Contains(msg.Content, want) {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	session, _ := store.GetSession(sessionID)
	t.Fatalf("session %s did not receive notification containing %q, last session = %+v", sessionID, want, session)
}

func taskHistory(session *Session, taskID string) []domain.Message {
	if session == nil {
		return nil
	}
	var out []domain.Message
	for _, msg := range session.GetMessages() {
		if strings.TrimSpace(msg.TaskID) == strings.TrimSpace(taskID) {
			out = append(out, msg)
		}
	}
	return out
}

func containsMessageContent(messages []domain.Message, want string) bool {
	for _, msg := range messages {
		if strings.Contains(msg.Content, want) {
			return true
		}
	}
	return false
}
