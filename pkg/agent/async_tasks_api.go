package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ListTasks returns recent async tasks across all sessions.
//
// Deprecated: use manager.Tasks().List(...) for canonical task.Task values.
func (m *Manager) ListTasks(limit int) []*AsyncTask {
	m.taskMu.RLock()
	out := make([]*AsyncTask, 0, len(m.asyncTasks))
	for _, task := range m.asyncTasks {
		if task != nil {
			out = append(out, cloneAsyncTask(task))
		}
	}
	m.taskMu.RUnlock()

	if len(out) == 0 {
		return nil
	}
	slicesSortAsyncTasks(out)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (m *Manager) AwaitTask(ctx context.Context, taskID string) (*UnifiedTask, error) {
	events, unsubscribe, err := m.SubscribeTask(taskID)
	if err != nil {
		return nil, err
	}
	defer unsubscribe()
	for {
		task, err := m.GetUnifiedTask(taskID)
		if err != nil {
			return nil, err
		}
		if isTerminalAsyncTaskStatus(AsyncTaskStatus(task.Status)) || isPausedAsyncTaskStatus(AsyncTaskStatus(task.Status)) {
			return task, nil
		}
		select {
		case <-ctx.Done():
			return task, ctx.Err()
		case _, ok := <-events:
			if !ok {
				return m.GetUnifiedTask(taskID)
			}
		}
	}
}

func (m *Manager) YieldTask(_ context.Context, taskID, reason string) (*UnifiedTask, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if isTerminalAsyncTaskStatus(task.Status) {
		return m.GetUnifiedTask(taskID)
	}
	now := time.Now()
	task = m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusYielded
		existing.Error = strings.TrimSpace(reason)
		if existing.StartedAt == nil {
			existing.StartedAt = &now
		}
	})
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeYielded,
		TeamID:    task.TeamID,
		TeamName:  task.TeamName,
		AgentName: firstNonEmptyTaskAgent(task),
		Message:   strings.TrimSpace(reason),
		Timestamp: now,
	}, false)
	return m.GetUnifiedTask(taskID)
}

func (m *Manager) ResumeTask(_ context.Context, taskID string, input any) (*UnifiedTask, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if !isPausedAsyncTaskStatus(task.Status) {
		return m.GetUnifiedTask(taskID)
	}
	now := time.Now()
	message := strings.TrimSpace(fmt.Sprint(input))
	resumePrompt := task.Prompt
	if message != "" {
		resumePrompt = strings.TrimSpace(resumePrompt + "\n\nResume input:\n" + message)
	}
	task = m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusResuming
		existing.Error = ""
		existing.Prompt = resumePrompt
		existing.FinishedAt = nil
	})
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeResumed,
		TeamID:    task.TeamID,
		TeamName:  task.TeamName,
		AgentName: firstNonEmptyTaskAgent(task),
		Message:   message,
		Timestamp: now,
	}, false)
	resumed, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	_ = resumed
	go m.runAsyncAgentTask(context.Background(), taskID)
	return m.GetUnifiedTask(taskID)
}

// CancelTask cancels a running or queued async task.
func (m *Manager) CancelTask(_ context.Context, taskID string) (*AsyncTask, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}

	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if isTerminalAsyncTaskStatus(task.Status) {
		return task, nil
	}

	m.taskMu.RLock()
	cancel := m.taskCancels[taskID]
	m.taskMu.RUnlock()
	if cancel != nil {
		cancel()
	}

	finishedAt := time.Now()
	task = m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusCancelled
		existing.FinishedAt = &finishedAt
		existing.Error = ""
		if strings.TrimSpace(existing.ResultText) == "" {
			existing.ResultText = "Task canceled."
		}
	})
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:           task.ID,
		SessionID:        task.SessionID,
		Kind:             task.Kind,
		Status:           task.Status,
		Type:             TaskEventTypeCancelled,
		TeamID:           task.TeamID,
		TeamName:         task.TeamName,
		OrchestratorName: task.OrchestratorName,
		AgentName:        firstNonEmptyTaskAgent(task),
		Message:          task.ResultText,
		Timestamp:        finishedAt,
	}, true)
	return task, nil
}

func firstNonEmptyTaskAgent(task *AsyncTask) string {
	if task == nil {
		return ""
	}
	if strings.TrimSpace(task.AgentName) != "" {
		return strings.TrimSpace(task.AgentName)
	}
	return strings.TrimSpace(task.OrchestratorName)
}
