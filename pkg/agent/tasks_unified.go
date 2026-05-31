package agent

import (
	"slices"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/store"
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

// ListUnifiedTasks returns the newest `limit` tasks in OLDEST-first order
// (the order existing CLI / task-service callers expect). Backed by SQL-level
// pagination, so it no longer over-fetches the whole table.
//
// NOTE: the list view (TaskSummary) does not include per-task message frames —
// only the detail endpoint (GetUnifiedTask) hydrates them. We deliberately skip
// hydrateUnifiedTask here; hydrating frames for every task is an N+1 query that
// made this endpoint hang with a few hundred tasks.
func (m *TeamManager) ListUnifiedTasks(limit int) []*UnifiedTask {
	tasks, _ := m.ListUnifiedTasksPaged(store.TaskListFilter{Limit: limit})
	// Paged results are newest-first; reverse to the historical oldest-first.
	for i, j := 0, len(tasks)-1; i < j; i, j = i+1, j-1 {
		tasks[i], tasks[j] = tasks[j], tasks[i]
	}
	return tasks
}

// ListUnifiedTasksPaged returns one newest-first page of tasks plus the total
// count matching the filter (ignoring limit/offset), for SQL-level pagination.
func (m *TeamManager) ListUnifiedTasksPaged(f store.TaskListFilter) ([]*UnifiedTask, int) {
	if m != nil && m.store != nil {
		if tasks, total, err := m.store.ListTasksPaged(f); err == nil && total > 0 {
			return tasks, total
		}
	}

	// Fallback: in-memory async tasks (no SQL store). Filter, sort newest-first,
	// then apply offset/limit in Go.
	asyncTasks := m.ListTasks(0)
	out := make([]*UnifiedTask, 0, len(asyncTasks))
	for _, task := range asyncTasks {
		unified := unifiedTaskFromAsync(task)
		if unified == nil || !matchesTaskFilter(unified, f) {
			continue
		}
		out = append(out, unified)
	}
	slices.SortFunc(out, func(a, b *UnifiedTask) int {
		return b.CreatedAt.Compare(a.CreatedAt) // newest first
	})
	total := len(out)

	start := f.Offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if f.Limit > 0 && start+f.Limit < end {
		end = start + f.Limit
	}
	return out[start:end], total
}

// matchesTaskFilter applies the same status/search constraints as the SQL
// WHERE clause, for the in-memory fallback path.
func matchesTaskFilter(t *UnifiedTask, f store.TaskListFilter) bool {
	if t == nil {
		return false
	}
	if status := strings.TrimSpace(f.Status); status != "" && !strings.EqualFold(status, "all") {
		if !strings.EqualFold(string(t.Status), status) {
			return false
		}
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		needle := strings.ToLower(search)
		hay := strings.ToLower(t.Input + " " + t.AgentName + " " + t.ID)
		if !strings.Contains(hay, needle) {
			return false
		}
	}
	return true
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
