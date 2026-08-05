package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AsyncTaskKind string

const (
	AsyncTaskKindAgent AsyncTaskKind = "agent"
	AsyncTaskKindTeam  AsyncTaskKind = "team"
)

type AsyncTaskStatus string

const (
	AsyncTaskStatusQueued    AsyncTaskStatus = "queued"
	AsyncTaskStatusRunning   AsyncTaskStatus = "running"
	AsyncTaskStatusWaiting   AsyncTaskStatus = "waiting"
	AsyncTaskStatusYielded   AsyncTaskStatus = "yielded"
	AsyncTaskStatusResumable AsyncTaskStatus = "resumable"
	AsyncTaskStatusResuming  AsyncTaskStatus = "resuming"
	AsyncTaskStatusCompleted AsyncTaskStatus = "completed"
	AsyncTaskStatusBlocked   AsyncTaskStatus = "blocked"
	AsyncTaskStatusFailed    AsyncTaskStatus = "failed"
	AsyncTaskStatusCancelled AsyncTaskStatus = "cancelled"
)

type TaskEventType string

const (
	TaskEventTypeCreated   TaskEventType = "created"
	TaskEventTypeQueued    TaskEventType = "queued"
	TaskEventTypeStarted   TaskEventType = "started"
	TaskEventTypeWaiting   TaskEventType = "waiting"
	TaskEventTypeYielded   TaskEventType = "yielded"
	TaskEventTypeResumed   TaskEventType = "resumed"
	TaskEventTypeRuntime   TaskEventType = "runtime"
	TaskEventTypeCompleted TaskEventType = "completed"
	TaskEventTypeBlocked   TaskEventType = "blocked"
	TaskEventTypeFailed    TaskEventType = "failed"
	TaskEventTypeCancelled TaskEventType = "cancelled"
)

// AsyncTask is a background task created by Dispatcher or direct pkg callers.
type AsyncTask struct {
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	Kind             AsyncTaskKind   `json:"kind"`
	Status           AsyncTaskStatus `json:"status"`
	TeamID           string          `json:"team_id,omitempty"`
	TeamName         string          `json:"team_name,omitempty"`
	OrchestratorName string          `json:"orchestrator_name,omitempty"`
	AgentName        string          `json:"agent_name,omitempty"`
	AgentNames       []string        `json:"agent_names,omitempty"`
	Prompt           string          `json:"prompt"`
	AckMessage       string          `json:"ack_message,omitempty"`
	ResultText       string          `json:"result_text,omitempty"`
	Error            string          `json:"error,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	Events           []*TaskEvent    `json:"events,omitempty"`

	// outputSchema, when set, forces the agent to produce a schema-validated
	// structured result for this task (StructuredOutput tool + validate +
	// retry). In-memory only; not serialized.
	outputSchema *StructuredOutputSpec
}

// TaskEvent is a task-level event that can wrap lower-level runtime events.
type TaskEvent struct {
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id"`
	SessionID        string          `json:"session_id,omitempty"`
	Kind             AsyncTaskKind   `json:"kind"`
	Status           AsyncTaskStatus `json:"status"`
	Type             TaskEventType   `json:"type"`
	TeamID           string          `json:"team_id,omitempty"`
	TeamName         string          `json:"team_name,omitempty"`
	OrchestratorName string          `json:"orchestrator_name,omitempty"`
	AgentName        string          `json:"agent_name,omitempty"`
	Message          string          `json:"message,omitempty"`
	Runtime          *Event          `json:"runtime,omitempty"`
	Timestamp        time.Time       `json:"timestamp"`
}

// submitAgentTaskWithSchema is SubmitAgentTask plus an optional per-task output
// schema. When schema != nil the agent is forced to emit a schema-validated
// structured result (validate + retry), so ResultText is guaranteed valid JSON.
func (m *Manager) submitAgentTaskWithSchema(ctx context.Context, sessionID, agentName, prompt string, schema *StructuredOutputSpec) (*AsyncTask, error) {
	agentName = strings.TrimSpace(agentName)
	prompt = strings.TrimSpace(prompt)
	if agentName == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if _, err := m.GetAgentByName(agentName); err != nil {
		return nil, err
	}

	taskID := uuid.NewString()
	task := &AsyncTask{
		ID:           taskID,
		TaskID:       taskID,
		SessionID:    strings.TrimSpace(sessionID),
		Kind:         AsyncTaskKindAgent,
		Status:       AsyncTaskStatusQueued,
		AgentName:    agentName,
		Prompt:       prompt,
		outputSchema: schema,
		AckMessage: fmt.Sprintf(
			"%s received that. It is running in the background.",
			agentName,
		),
		CreatedAt: time.Now(),
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

	go m.runAsyncAgentTask(context.WithoutCancel(ctx), task.ID)

	return m.GetTask(task.ID)
}

// GetTask returns a legacy AsyncTask view.
//
// Deprecated: use manager.Tasks().Get(...) for the canonical task.Task.
func (m *Manager) GetTask(taskID string) (*AsyncTask, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()

	task := m.asyncTasks[taskID]
	if task == nil {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return cloneAsyncTask(task), nil
}

func (m *Manager) SubscribeTask(taskID string) (<-chan *TaskEvent, func(), error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil, fmt.Errorf("task id is required")
	}

	m.taskMu.Lock()
	task := m.asyncTasks[taskID]
	if task == nil {
		m.taskMu.Unlock()
		return nil, nil, fmt.Errorf("task %s not found", taskID)
	}
	backlog := cloneTaskEvents(task.Events)
	terminal := isTerminalAsyncTaskStatus(task.Status)
	ch := make(chan *TaskEvent, max(16, len(backlog)+4))
	if !terminal {
		if m.taskSubs[taskID] == nil {
			m.taskSubs[taskID] = make(map[chan *TaskEvent]struct{})
		}
		m.taskSubs[taskID][ch] = struct{}{}
	}
	m.taskMu.Unlock()

	for _, evt := range backlog {
		ch <- evt
	}
	if terminal {
		close(ch)
		return ch, func() {}, nil
	}

	unsubscribe := func() {
		m.taskMu.Lock()
		defer m.taskMu.Unlock()
		shouldClose := false
		if subs := m.taskSubs[taskID]; subs != nil {
			if _, ok := subs[ch]; ok {
				delete(subs, ch)
				shouldClose = true
				if len(subs) == 0 {
					delete(m.taskSubs, taskID)
				}
			}
		}
		if shouldClose {
			close(ch)
		}
	}
	return ch, unsubscribe, nil
}

func (m *Manager) runAsyncAgentTask(ctx context.Context, taskID string) {
	task, err := m.GetTask(taskID)
	if err != nil {
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	m.setTaskCancel(task.ID, cancel)
	defer m.clearTaskCancel(task.ID)

	startedAt := time.Now()
	task = m.updateAsyncTask(task.ID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusRunning
		existing.StartedAt = &startedAt
		if strings.TrimSpace(existing.TaskID) == "" {
			existing.TaskID = existing.ID
		}
	})
	m.emitTaskEvent(task.ID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeStarted,
		AgentName: task.AgentName,
		Message:   fmt.Sprintf("%s started background work.", task.AgentName),
		Timestamp: startedAt,
	}, false)

	runOpts := []RunOption{WithTaskID(firstNonEmptyTaskID(task))}
	if task.outputSchema != nil {
		runOpts = append(runOpts, WithStructuredOutput(task.outputSchema))
	}
	runOpts = append(runOpts, WithSessionID(m.sessionIDFor(task.SessionID, task.AgentName)))
	events, err := m.RunStream(runCtx, task.AgentName, task.Prompt, runOpts...)
	if err != nil {
		m.failAsyncTask(task.ID, task.AgentName, err)
		return
	}

	finalText, blocked, runErr := m.forwardRuntimeEvents(task.ID, events)
	if runErr != nil {
		m.failAsyncTask(task.ID, task.AgentName, runErr)
		return
	}
	if blocked {
		m.blockAsyncTask(task.ID, finalText, task.AgentName)
		return
	}
	m.completeAsyncTask(task.ID, finalText, task.AgentName)
}

func (m *Manager) forwardRuntimeEvents(taskID string, events <-chan *Event) (string, bool, error) {
	var finalText string
	blocked := false
	for evt := range events {
		runtimeEvt := cloneAgentEvent(evt)
		m.emitTaskEvent(taskID, &TaskEvent{
			TaskID:    taskID,
			Type:      TaskEventTypeRuntime,
			AgentName: runtimeEvt.AgentName,
			Runtime:   runtimeEvt,
			Timestamp: runtimeEvt.Timestamp,
		}, false)

		switch runtimeEvt.Type {
		case EventTypeComplete:
			finalText = strings.TrimSpace(runtimeEvt.Content)
		case EventTypeBlocked:
			finalText = strings.TrimSpace(runtimeEvt.Content)
			blocked = true
		case EventTypeError:
			msg := strings.TrimSpace(runtimeEvt.Content)
			if msg == "" {
				msg = "agent execution failed"
			}
			return finalText, blocked, errors.New(msg)
		}
	}
	return finalText, blocked, nil
}

func (m *Manager) completeAsyncTask(taskID, finalText, agentName string) {
	finishedAt := time.Now()
	task := m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusCompleted
		existing.ResultText = strings.TrimSpace(finalText)
		existing.FinishedAt = &finishedAt
		existing.Error = ""
	})
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeCompleted,
		TeamID:    task.TeamID,
		TeamName:  task.TeamName,
		AgentName: agentName,
		Message:   task.ResultText,
		Timestamp: finishedAt,
	}, true)
}

func (m *Manager) blockAsyncTask(taskID, blocker, agentName string) {
	finishedAt := time.Now()
	task := m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusBlocked
		existing.ResultText = strings.TrimSpace(blocker)
		existing.Error = strings.TrimSpace(blocker)
		existing.FinishedAt = &finishedAt
	})
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeBlocked,
		TeamID:    task.TeamID,
		TeamName:  task.TeamName,
		AgentName: agentName,
		Message:   task.ResultText,
		Timestamp: finishedAt,
	}, true)
}

func (m *Manager) failAsyncTask(taskID, agentName string, err error) {
	finishedAt := time.Now()
	transitioned := false
	task := m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		// A concurrent CancelTask (or another finisher) may have already put
		// the task in a terminal state — never overwrite it with "failed".
		if isTerminalAsyncTaskStatus(existing.Status) {
			return
		}
		transitioned = true
		existing.Status = AsyncTaskStatusFailed
		existing.Error = strings.TrimSpace(err.Error())
		existing.FinishedAt = &finishedAt
	})
	if !transitioned {
		return
	}
	m.emitTaskEvent(taskID, &TaskEvent{
		TaskID:    task.ID,
		SessionID: task.SessionID,
		Kind:      task.Kind,
		Status:    task.Status,
		Type:      TaskEventTypeFailed,
		TeamID:    task.TeamID,
		TeamName:  task.TeamName,
		AgentName: agentName,
		Message:   task.Error,
		Timestamp: finishedAt,
	}, true)
}
