package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

const persistedAsyncTaskSource = "delegate_async"

// executeDelegateAsync spawns a sub-agent asynchronously and returns immediately.
func (s *Service) executeDelegateAsync(ctx context.Context, currentAgent *Agent, args map[string]interface{}) (interface{}, error) {
	goal, _ := args["goal"].(string)
	name, _ := args["name"].(string)

	if goal == "" || name == "" {
		return nil, fmt.Errorf("delegate_async: 'goal' and 'name' arguments are required")
	}
	if s.store == nil {
		return nil, fmt.Errorf("delegate_async: persistent store is not configured")
	}

	session := getCurrentSession(ctx)
	agentName := s.persistedAsyncAgentName(currentAgent)
	if agentName == "" {
		return nil, fmt.Errorf("delegate_async: current agent is not available")
	}
	taskID := uuid.NewString()
	runtimeSessionID := uuid.NewString()
	parentTaskID := currentTaskID(session)
	now := time.Now()

	task := &taskpkg.Task{
		ID:               taskID,
		Kind:             taskpkg.KindAgent,
		Status:           taskpkg.StatusQueued,
		SessionID:        runtimeSessionID,
		RuntimeSessionID: runtimeSessionID,
		ParentTaskID:     strings.TrimSpace(parentTaskID),
		ContinuationID:   taskID,
		QueueClass:       taskpkg.QueueClassTask,
		AgentName:        agentName,
		Input:            strings.TrimSpace(goal),
		CreatedAt:        now,
		Source:           persistedAsyncTaskSource,
		SourceID:         strings.TrimSpace(name),
	}
	if err := s.store.SaveTask(task); err != nil {
		return nil, fmt.Errorf("delegate_async: save task: %w", err)
	}

	s.runPersistedAsyncTask(task.ID, task.RuntimeSessionID, task.ParentTaskID, goal)
	s.watchPersistedAsyncTask(notificationSessionID(session), task.ID, fmt.Sprintf(`Async SubAgent "%s" finished`, name))

	s.emitProgress("tool_call", fmt.Sprintf("→ Spawned Async SubAgent: %s", name), 0, "delegate_async")

	return map[string]interface{}{
		"status":  "spawned",
		"task_id": task.ID,
		"message": fmt.Sprintf("Sub-agent '%s' spawned successfully and is running in the background. Do not wait for it. You will receive a <task-notification> message when it finishes. You can continue with other work.", name),
	}, nil
}

// executeSendMessage handles sending a message to a running or paused async task
func (s *Service) executeSendMessage(ctx context.Context, currentAgent *Agent, args map[string]interface{}) (interface{}, error) {
	taskID, _ := args["to"].(string)
	message, _ := args["message"].(string)

	if taskID == "" || message == "" {
		return nil, fmt.Errorf("send_message: 'to' and 'message' arguments are required")
	}
	if s.store == nil {
		return nil, fmt.Errorf("send_message: persistent store is not configured")
	}

	task, err := s.loadPersistedAsyncTask(taskID)
	if err != nil {
		return nil, fmt.Errorf("send_message: %w", err)
	}
	if task.AgentName != "" {
		currentName := s.persistedAsyncAgentName(currentAgent)
		if currentName != "" && !strings.EqualFold(strings.TrimSpace(task.AgentName), currentName) {
			return nil, fmt.Errorf("send_message: task '%s' belongs to agent '%s', current agent is '%s'", taskID, strings.TrimSpace(task.AgentName), currentName)
		}
	}
	switch task.Status {
	case taskpkg.StatusQueued, taskpkg.StatusRunning, taskpkg.StatusResuming:
		return nil, fmt.Errorf("send_message: task '%s' is currently in status '%s', cannot send message right now", taskID, task.Status)
	}

	s.emitProgress("tool_call", fmt.Sprintf("→ Sending message to SubAgent %s", taskID[:8]), 0, "send_message")

	session := getCurrentSession(ctx)
	task.Status = taskpkg.StatusResuming
	task.QueueClass = taskpkg.QueueClassMicrotask
	task.Awaiting = nil
	task.Error = ""
	task.FinishedAt = nil
	if err := s.store.SaveTask(task); err != nil {
		return nil, fmt.Errorf("send_message: save task: %w", err)
	}

	runtimeSessionID := firstNonEmptyTaskString(task.RuntimeSessionID, task.SessionID)
	if runtimeSessionID == "" {
		return nil, fmt.Errorf("send_message: task '%s' is missing a runtime session", taskID)
	}

	s.runPersistedAsyncTask(task.ID, runtimeSessionID, task.ParentTaskID, message)
	s.watchPersistedAsyncTask(notificationSessionID(session), task.ID, "Async SubAgent response to your message")

	return map[string]interface{}{
		"status":  "message_sent",
		"task_id": task.ID,
		"message": "Message sent. The sub-agent has resumed working in the background. You will receive another <task-notification> when it finishes this new task.",
	}, nil
}

func (s *Service) persistedAsyncAgentName(currentAgent *Agent) string {
	if currentAgent != nil && strings.TrimSpace(currentAgent.Name()) != "" {
		return strings.TrimSpace(currentAgent.Name())
	}
	if s != nil && s.agent != nil && strings.TrimSpace(s.agent.Name()) != "" {
		return strings.TrimSpace(s.agent.Name())
	}
	return ""
}

func notificationSessionID(session *Session) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.GetID())
}

func (s *Service) loadPersistedAsyncTask(taskID string) (*taskpkg.Task, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("task store unavailable")
	}
	task, err := s.store.GetTask(strings.TrimSpace(taskID))
	if err != nil || task == nil {
		return nil, fmt.Errorf("no async task found with ID '%s'", strings.TrimSpace(taskID))
	}
	if strings.TrimSpace(task.Source) != persistedAsyncTaskSource {
		return nil, fmt.Errorf("task '%s' is not a persisted async continuation", strings.TrimSpace(taskID))
	}
	return task, nil
}

func (s *Service) runPersistedAsyncTask(taskID, runtimeSessionID, parentTaskID, goal string) {
	if s == nil {
		return
	}
	go func() {
		events, err := s.RunStreamWithOptions(context.Background(), strings.TrimSpace(goal),
			WithSessionID(strings.TrimSpace(runtimeSessionID)),
			WithTaskID(strings.TrimSpace(taskID)),
			WithParentTaskID(strings.TrimSpace(parentTaskID)),
		)
		if err != nil {
			if s.store == nil {
				return
			}
			task, loadErr := s.store.GetTask(strings.TrimSpace(taskID))
			if loadErr != nil || task == nil {
				task = &taskpkg.Task{
					ID:               strings.TrimSpace(taskID),
					Kind:             taskpkg.KindAgent,
					SessionID:        strings.TrimSpace(runtimeSessionID),
					RuntimeSessionID: strings.TrimSpace(runtimeSessionID),
					ParentTaskID:     strings.TrimSpace(parentTaskID),
					ContinuationID:   strings.TrimSpace(taskID),
					CreatedAt:        time.Now(),
					Source:           persistedAsyncTaskSource,
				}
			}
			task.Status = taskpkg.StatusFailed
			task.Error = strings.TrimSpace(err.Error())
			finishedAt := time.Now()
			task.FinishedAt = &finishedAt
			_ = s.store.SaveTask(task)
			return
		}
		for range events {
		}
	}()
}

func (s *Service) watchPersistedAsyncTask(sessionID, taskID, summary string) {
	if s == nil || s.store == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(taskID) == "" {
		return
	}
	go func() {
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			task, err := s.store.GetTask(strings.TrimSpace(taskID))
			if err == nil && task != nil {
				switch task.Status {
				case taskpkg.StatusCompleted, taskpkg.StatusBlocked, taskpkg.StatusFailed, taskpkg.StatusCancelled:
					result := strings.TrimSpace(firstNonEmptyTaskString(task.Output, task.Error))
					if result == "" {
						result = "(no output)"
					}
					notification := fmt.Sprintf(`<task-notification>
<task-id>%s</task-id>
<status>%s</status>
<summary>%s</summary>
<result>%s</result>
</task-notification>`, task.ID, task.Status, strings.TrimSpace(summary), result)
					session, loadErr := s.store.GetSession(strings.TrimSpace(sessionID))
					if loadErr == nil && session != nil {
						session.AddMessage(domain.Message{
							Role:    "user",
							Content: notification,
						})
						_ = s.store.SaveSession(session)
					}
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
}
