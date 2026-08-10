package agent

import (
	"slices"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/store"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
)

type TaskKind = taskpkg.Kind
type UnifiedTaskMessage = taskpkg.Frame
type UnifiedTask = taskpkg.Task

const (
	TaskKindAgent     = taskpkg.KindAgent
	TaskKindTeam      = taskpkg.KindTeam
	TaskKindScheduler = taskpkg.KindScheduler
)

func (m *Manager) GetUnifiedTask(taskID string) (*UnifiedTask, error) {
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
func (m *Manager) ListUnifiedTasks(limit int) []*UnifiedTask {
	tasks, _ := m.ListUnifiedTasksPaged(store.TaskListFilter{Limit: limit})
	// Paged results are newest-first; reverse to the historical oldest-first.
	for i, j := 0, len(tasks)-1; i < j; i, j = i+1, j-1 {
		tasks[i], tasks[j] = tasks[j], tasks[i]
	}
	return tasks
}

// ListUnifiedTasksPaged returns one newest-first page of tasks plus the total
// count matching the filter (ignoring limit/offset), for SQL-level pagination.
func (m *Manager) ListUnifiedTasksPaged(f store.TaskListFilter) ([]*UnifiedTask, int) {
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

// persistUnifiedTaskSnapshotHook is a test seam. It fires after the in-memory
// snapshot has been taken and before the row is written, which is the window
// the bug lived in. Tests use it to hold one writer there while another runs to
// completion. Nil in production.
var persistUnifiedTaskSnapshotHook func(taskID string)

// persistUnifiedTask mirrors the in-memory async task onto its stored row.
//
// This is the fifth writer of a task row, and the last one that was still doing
// an unserialised read-modify-write. c848ae1 converted the four on the run path;
// this one reads from a different source — the manager's async map rather than
// the row itself — which is why it did not look like the same shape. It is.
//
// The window was wide because hydrateUnifiedTask queries the message store
// between the snapshot and the write. updateAsyncTask fires this as a bare
// goroutine on every mutation, so a resume did:
//
//	P1  snapshot{running, output:""} ... blocked in ListMessagesForTask ...
//	P2  snapshot{completed, output:"final: 42"} -> write
//	P1                                          -> write   (stale, wins)
//
// leaving the row permanently running with no output — which is exactly what
// CI reported, and why widening the poll deadline twice never helped: nothing
// writes that row again.
//
// Taking the snapshot inside the store's per-task lock is what fixes it: the
// last writer to acquire the lock is now, necessarily, the one holding the
// freshest snapshot. It also puts this writer under the same lock as the run
// path, so the two can no longer clobber each other.
func (m *Manager) persistUnifiedTask(taskID string) {
	if m == nil || m.store == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	_ = m.store.updateTask(taskID, func(existing *UnifiedTask) *UnifiedTask {
		m.taskMu.RLock()
		task := cloneAsyncTask(m.asyncTasks[taskID])
		m.taskMu.RUnlock()

		if hook := persistUnifiedTaskSnapshotHook; hook != nil {
			hook(taskID)
		}

		unified := unifiedTaskFromAsync(task)
		if unified == nil {
			return nil
		}
		m.hydrateUnifiedTask(unified)
		carryOverTaskFields(existing, unified)
		return unified
	})
}

// carryOverTaskFields preserves the parts of a task row that the async-task
// mirror does not own. It builds its row from the AsyncTask alone, which knows
// nothing about stats, lineage, or the runtime's frames — so without this, a
// status update would silently erase them.
func carryOverTaskFields(existing, updated *UnifiedTask) {
	if existing == nil || updated == nil {
		return
	}
	if updated.Stats == nil {
		updated.Stats = existing.Stats
	}
	if strings.TrimSpace(updated.ParentTaskID) == "" {
		updated.ParentTaskID = existing.ParentTaskID
	}
	// Frames come from the message store; when that lookup finds nothing yet,
	// keep whatever the runtime has already written rather than blanking it.
	if len(updated.Frames) == 0 {
		updated.Frames = existing.Frames
	}
	if len(updated.Events) == 0 {
		updated.Events = existing.Events
	}
	if updated.CreatedAt.IsZero() {
		updated.CreatedAt = existing.CreatedAt
	}
	if updated.StartedAt == nil {
		updated.StartedAt = existing.StartedAt
	}
	if strings.TrimSpace(updated.Input) == "" {
		updated.Input = existing.Input
	}
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

func (m *Manager) hydrateUnifiedTask(task *UnifiedTask) {
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
