package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type FollowUpAgentPolicy struct {
	HookDescription string
	AgentName       string
	Priority        int
	ScenarioTags    []string
	ShouldDispatch  func(HookData) bool
	BuildPrompt     func(HookData) string
	BuildSupplement func(HookData) (string, bool)
}

func (m *TeamManager) ConfigureFollowUpAgentHook(svc *Service, policy FollowUpAgentPolicy) {
	if m == nil || svc == nil || svc.hooks == nil {
		return
	}
	if strings.TrimSpace(policy.AgentName) == "" || strings.TrimSpace(policy.HookDescription) == "" {
		return
	}
	if _, err := m.GetAgentByName(policy.AgentName); err != nil {
		return
	}
	if serviceHasHookDescription(svc, HookEventPostExecution, policy.HookDescription) {
		return
	}

	priority := policy.Priority
	if priority == 0 {
		priority = 100
	}

	svc.hooks.Register(HookEventPostExecution, func(ctx context.Context, event HookEvent, data HookData) (interface{}, error) {
		if event != HookEventPostExecution {
			return nil, nil
		}
		if policy.ShouldDispatch != nil && !policy.ShouldDispatch(data) {
			return nil, nil
		}

		if policy.BuildSupplement != nil {
			if supplement, ok := policy.BuildSupplement(data); ok {
				task := m.submitSyntheticFollowUpTask(strings.TrimSpace(data.SessionID), policy.AgentName, supplement)
				return asyncTaskHookResult(task, policy), nil
			}
		}

		if policy.BuildPrompt == nil {
			return nil, nil
		}
		prompt := strings.TrimSpace(policy.BuildPrompt(data))
		if prompt == "" {
			return nil, nil
		}

		task, err := m.SubmitAgentTask(context.Background(), strings.TrimSpace(data.SessionID), policy.AgentName, prompt)
		if err != nil || task == nil {
			return nil, nil
		}
		return asyncTaskHookResult(task, policy), nil
	}, WithHookDescription(policy.HookDescription), WithHookPriority(priority))
}

func serviceHasHookDescription(svc *Service, event HookEvent, description string) bool {
	if svc == nil || svc.hooks == nil {
		return false
	}
	for _, hook := range svc.hooks.List(event) {
		if hook != nil && strings.TrimSpace(hook.Description) == strings.TrimSpace(description) {
			return true
		}
	}
	return false
}

func asyncTaskHookResult(task *AsyncTask, policy FollowUpAgentPolicy) map[string]interface{} {
	if task == nil {
		return nil
	}
	return map[string]interface{}{
		"type":             "async_agent_task",
		"task_id":          task.ID,
		"agent_name":       policy.AgentName,
		"priority":         policy.Priority,
		"scenario_tags":    append([]string(nil), policy.ScenarioTags...),
		"hook_description": policy.HookDescription,
	}
}

func (m *TeamManager) submitSyntheticFollowUpTask(sessionID, agentName, text string) *AsyncTask {
	text = strings.TrimSpace(text)
	agentName = strings.TrimSpace(agentName)
	if m == nil || text == "" || agentName == "" {
		return nil
	}

	task := &AsyncTask{
		ID:         uuid.NewString(),
		TaskID:     uuid.NewString(),
		SessionID:  strings.TrimSpace(sessionID),
		Kind:       AsyncTaskKindAgent,
		Status:     AsyncTaskStatusQueued,
		AgentName:  agentName,
		Prompt:     "synthetic follow-up supplement",
		AckMessage: fmt.Sprintf("%s received that. It is running in the background.", agentName),
		CreatedAt:  time.Now(),
	}
	m.upsertAsyncTask(task)
	m.emitTaskEvent(task.ID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeCreated,
		AgentName: task.AgentName,
		Message:   task.AckMessage,
		Timestamp: task.CreatedAt,
	}, false)

	go func(taskID, taskSessionID, taskAgentName, message string) {
		startedAt := time.Now()
		m.updateAsyncTask(taskID, func(existing *AsyncTask) {
			existing.Status = AsyncTaskStatusRunning
			existing.StartedAt = &startedAt
		})
		m.emitTaskEvent(taskID, &TaskEvent{
			TaskID:    taskID,
			SessionID: taskSessionID,
			Kind:      AsyncTaskKindAgent,
			Status:    AsyncTaskStatusRunning,
			Type:      TaskEventTypeStarted,
			AgentName: taskAgentName,
			Message:   fmt.Sprintf("%s started background work.", taskAgentName),
			Timestamp: startedAt,
		}, false)
		m.completeAsyncTask(taskID, message, taskAgentName)
	}(task.ID, task.SessionID, task.AgentName, text)

	return task
}
