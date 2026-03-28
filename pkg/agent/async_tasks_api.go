package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ListTasks returns recent async tasks across all sessions.
func (m *TeamManager) ListTasks(limit int) []*AsyncTask {
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

// CancelTask cancels a running or queued async task.
func (m *TeamManager) CancelTask(_ context.Context, taskID string) (*AsyncTask, error) {
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
		TaskID:      task.ID,
		SessionID:   task.SessionID,
		Kind:        task.Kind,
		Status:      task.Status,
		Type:        TaskEventTypeCancelled,
		TeamID:     task.TeamID,
		TeamName:   task.TeamName,
		CaptainName: task.CaptainName,
		AgentName:   firstNonEmptyTaskAgent(task),
		Message:     task.ResultText,
		Timestamp:   finishedAt,
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
	return strings.TrimSpace(task.CaptainName)
}
