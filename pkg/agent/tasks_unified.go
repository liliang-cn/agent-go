package agent

import (
	"slices"
	"strings"

	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

type TaskKind = taskpkg.Kind
type UnifiedTaskMessage = taskpkg.Frame
type UnifiedTask = taskpkg.Task

const (
	TaskKindAgent     = taskpkg.KindAgent
	TaskKindTeam      = taskpkg.KindTeam
	TaskKindScheduler = taskpkg.KindScheduler
)

func (m *TeamManager) GetUnifiedTask(taskID string) (*UnifiedTask, error) {
	if m != nil && m.store != nil {
		if task, err := m.store.GetTask(taskID); err == nil && task != nil {
			m.hydrateUnifiedTask(task)
			return task, nil
		}
	}
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	unified := unifiedTaskFromAsync(task)
	m.hydrateUnifiedTask(unified)
	return unified, nil
}

func (m *TeamManager) ListUnifiedTasks(limit int) []*UnifiedTask {
	// NOTE: the list view (TaskSummary) does not include per-task message
	// frames — only the detail endpoint (GetUnifiedTask) hydrates them.
	// So we deliberately skip hydrateUnifiedTask here; hydrating frames for
	// every task is an N+1 query that made this endpoint hang with a few
	// hundred tasks (one ListMessagesForTask per task).
	if m != nil && m.store != nil {
		if tasks, err := m.store.ListTasks(max(limit, 1000)); err == nil && len(tasks) > 0 {
			slices.SortFunc(tasks, func(a, b *UnifiedTask) int {
				return a.CreatedAt.Compare(b.CreatedAt)
			})
			if limit > 0 && len(tasks) > limit {
				tasks = tasks[len(tasks)-limit:]
			}
			return tasks
		}
	}

	asyncTasks := m.ListTasks(0)
	out := make([]*UnifiedTask, 0, len(asyncTasks))
	for _, task := range asyncTasks {
		if unified := unifiedTaskFromAsync(task); unified != nil {
			out = append(out, unified)
		}
	}
	slices.SortFunc(out, func(a, b *UnifiedTask) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (m *TeamManager) persistUnifiedTask(taskID string) {
	if m == nil || m.store == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	m.taskMu.RLock()
	task := cloneAsyncTask(m.asyncTasks[taskID])
	m.taskMu.RUnlock()
	unified := unifiedTaskFromAsync(task)
	if unified == nil {
		return
	}
	m.hydrateUnifiedTask(unified)
	_ = m.store.SaveTask(unified)
}

func unifiedTaskFromAsync(task *AsyncTask) *UnifiedTask {
	if task == nil {
		return nil
	}
	kind := TaskKind(task.Kind)
	if kind == "" {
		kind = taskpkg.KindAgent
	}
	return &UnifiedTask{
		ID:               firstNonEmptyTaskString(task.TaskID, task.ID),
		Kind:             kind,
		Status:           taskpkg.Status(task.Status),
		SessionID:        strings.TrimSpace(task.SessionID),
		RuntimeSessionID: strings.TrimSpace(task.SessionID),
		ContinuationID:   firstNonEmptyTaskString(task.TaskID, task.ID),
		QueueClass:       queueClassFromAsync(task),
		Awaiting:         awaitingStateFromAsync(task),
		TeamID:           strings.TrimSpace(task.TeamID),
		TeamName:         strings.TrimSpace(task.TeamName),
		AgentName:        firstNonEmptyTaskString(task.AgentName, task.OrchestratorName),
		AgentNames:       append([]string(nil), task.AgentNames...),
		Input:            task.Prompt,
		Output:           task.ResultText,
		Error:            task.Error,
		CreatedAt:        task.CreatedAt,
		StartedAt:        cloneTimePtr(task.StartedAt),
		FinishedAt:       cloneTimePtr(task.FinishedAt),
		Events:           convertTaskEvents(task.Events),
		Source:           "async_task",
		SourceID:         task.ID,
	}
}

func queueClassFromAsync(task *AsyncTask) taskpkg.QueueClass {
	if task == nil {
		return taskpkg.QueueClassTask
	}
	if task.Status == AsyncTaskStatusResuming {
		return taskpkg.QueueClassMicrotask
	}
	return taskpkg.QueueClassTask
}

func awaitingStateFromAsync(task *AsyncTask) *taskpkg.AwaitingState {
	if task == nil || !isPausedAsyncTaskStatus(task.Status) {
		return nil
	}
	since := task.CreatedAt
	if task.StartedAt != nil {
		since = *task.StartedAt
	}
	return &taskpkg.AwaitingState{
		Type:      "resume",
		Reason:    task.Error,
		Since:     since,
		AgentName: firstNonEmptyTaskString(task.AgentName, task.OrchestratorName),
	}
}

func (m *TeamManager) hydrateUnifiedTask(task *UnifiedTask) {
	if m == nil || task == nil || m.store == nil {
		return
	}
	messages, _ := m.store.ListMessagesForTask(task.ID, 500)
	task.Frames = messages
}

func convertTaskEvents(events []*TaskEvent) []taskpkg.Event {
	out := make([]taskpkg.Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		out = append(out, taskpkg.Event{
			ID:               event.ID,
			TaskID:           event.TaskID,
			SessionID:        event.SessionID,
			Kind:             taskpkg.Kind(event.Kind),
			Status:           taskpkg.Status(event.Status),
			Type:             string(event.Type),
			TeamID:           event.TeamID,
			TeamName:         event.TeamName,
			OrchestratorName: event.OrchestratorName,
			AgentName:        event.AgentName,
			Message:          event.Message,
			Timestamp:        event.Timestamp,
		})
	}
	return out
}

func firstNonEmptyTaskString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
