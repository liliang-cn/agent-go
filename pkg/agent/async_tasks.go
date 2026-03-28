package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	AsyncTaskStatusCompleted AsyncTaskStatus = "completed"
	AsyncTaskStatusFailed    AsyncTaskStatus = "failed"
	AsyncTaskStatusCancelled AsyncTaskStatus = "cancelled"
)

type TaskEventType string

const (
	TaskEventTypeCreated   TaskEventType = "created"
	TaskEventTypeQueued    TaskEventType = "queued"
	TaskEventTypeStarted   TaskEventType = "started"
	TaskEventTypeRuntime   TaskEventType = "runtime"
	TaskEventTypeCompleted TaskEventType = "completed"
	TaskEventTypeFailed    TaskEventType = "failed"
	TaskEventTypeCancelled TaskEventType = "cancelled"
)

// AsyncTask is a background task created by Concierge or direct pkg callers.
type AsyncTask struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id,omitempty"`
	Kind        AsyncTaskKind   `json:"kind"`
	Status      AsyncTaskStatus `json:"status"`
	TeamID      string          `json:"team_id,omitempty"`
	TeamName    string          `json:"team_name,omitempty"`
	CaptainName string          `json:"captain_name,omitempty"`
	AgentName   string          `json:"agent_name,omitempty"`
	AgentNames  []string        `json:"agent_names,omitempty"`
	Prompt      string          `json:"prompt"`
	AckMessage  string          `json:"ack_message,omitempty"`
	ResultText  string          `json:"result_text,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	Events      []*TaskEvent    `json:"events,omitempty"`
}

// TaskEvent is a task-level event that can wrap lower-level runtime events.
type TaskEvent struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	SessionID   string          `json:"session_id,omitempty"`
	Kind        AsyncTaskKind   `json:"kind"`
	Status      AsyncTaskStatus `json:"status"`
	Type        TaskEventType   `json:"type"`
	TeamID      string          `json:"team_id,omitempty"`
	TeamName    string          `json:"team_name,omitempty"`
	CaptainName string          `json:"captain_name,omitempty"`
	AgentName   string          `json:"agent_name,omitempty"`
	Message     string          `json:"message,omitempty"`
	Runtime     *Event          `json:"runtime,omitempty"`
	Timestamp   time.Time       `json:"timestamp"`
}

func (m *TeamManager) SubmitAgentTask(ctx context.Context, sessionID, agentName, prompt string) (*AsyncTask, error) {
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

	task := &AsyncTask{
		ID:        uuid.NewString(),
		SessionID: strings.TrimSpace(sessionID),
		Kind:      AsyncTaskKindAgent,
		Status:    AsyncTaskStatusQueued,
		AgentName: agentName,
		Prompt:    prompt,
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

func (m *TeamManager) SubmitTeamTask(ctx context.Context, sessionID, teamID, prompt string, agentNames []string) (*AsyncTask, error) {
	team, err := m.resolveTeamRef(strings.TrimSpace(teamID), "")
	if err != nil {
		return nil, err
	}
	lead, err := m.GetLeadAgentForTeam(team.ID)
	if err != nil {
		return nil, err
	}

	sharedTask, err := m.EnqueueSharedTaskForTeam(ctx, team.ID, lead.Name, agentNames, prompt)
	if err != nil {
		return nil, err
	}

	task := m.ensureAsyncTaskForSharedTask(sharedTask, strings.TrimSpace(sessionID), team.Name)
	m.emitTaskEvent(task.ID, &TaskEvent{
		TaskID:      task.ID,
		SessionID:   task.SessionID,
		Kind:        task.Kind,
		Status:      task.Status,
		Type:        TaskEventTypeCreated,
		TeamID:      task.TeamID,
		TeamName:    task.TeamName,
		CaptainName: task.CaptainName,
		AgentName:   task.CaptainName,
		Message:     task.AckMessage,
		Timestamp:   task.CreatedAt,
	}, false)

	return m.GetTask(task.ID)
}

func (m *TeamManager) GetTask(taskID string) (*AsyncTask, error) {
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

func (m *TeamManager) ListSessionTasks(sessionID string, limit int) []*AsyncTask {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	m.taskMu.RLock()
	taskIDs := append([]string(nil), m.sessionTasks[sessionID]...)
	out := make([]*AsyncTask, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if task := m.asyncTasks[taskID]; task != nil {
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

func (m *TeamManager) SubscribeTask(taskID string) (<-chan *TaskEvent, func(), error) {
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

func (m *TeamManager) runAsyncAgentTask(ctx context.Context, taskID string) {
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

	events, err := m.ChatWithMemberStream(runCtx, task.SessionID, task.AgentName, task.Prompt)
	if err != nil {
		m.failAsyncTask(task.ID, task.AgentName, err)
		return
	}

	finalText, runErr := m.forwardRuntimeEvents(task.ID, events)
	if runErr != nil {
		m.failAsyncTask(task.ID, task.AgentName, runErr)
		return
	}
	m.completeAsyncTask(task.ID, finalText, task.AgentName)
}

func (m *TeamManager) executeSharedTaskStream(ctx context.Context, task *SharedTask) {
	type dispatchResult struct {
		AgentName string
		Text      string
		Err       error
	}

	asyncTask := m.ensureAsyncTaskForSharedTask(task, "", "")
	startedAt := time.Now()
	asyncTask = m.updateAsyncTask(asyncTask.ID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusRunning
		existing.StartedAt = &startedAt
	})
	m.emitTaskEvent(asyncTask.ID, &TaskEvent{
		TaskID:      asyncTask.ID,
		SessionID:   asyncTask.SessionID,
		Kind:        asyncTask.Kind,
		Status:      asyncTask.Status,
		Type:        TaskEventTypeStarted,
		TeamID:      asyncTask.TeamID,
		TeamName:    asyncTask.TeamName,
		CaptainName: asyncTask.CaptainName,
		AgentName:   asyncTask.CaptainName,
		Message:     fmt.Sprintf("%s started team task.", asyncTask.CaptainName),
		Timestamp:   startedAt,
	}, false)

	results := make([]SharedTaskResult, 0, len(task.AgentNames))
	resultTextParts := make([]string, 0, len(task.AgentNames))
	resultCh := make(chan dispatchResult, len(task.AgentNames))
	var wg sync.WaitGroup

	for _, agentName := range task.AgentNames {
		agentName := agentName
		wg.Add(1)
		go func() {
			defer wg.Done()
			instruction := task.Prompt
			if strings.TrimSpace(agentName) != "" {
				instruction = strings.TrimSpace(
					"Assigned team member: " + agentName + "\n" +
						"You are responsible only for the work that fits your own role and the target agent label above.\n" +
						"Do not complete work that belongs to other listed team members.\n\n" +
						task.Prompt,
				)
			}
			events, err := m.ChatWithMemberStream(ctx, task.ID, agentName, instruction)
			if err != nil {
				resultCh <- dispatchResult{AgentName: agentName, Err: err}
				return
			}
			text, runErr := m.forwardRuntimeEvents(task.ID, events)
			resultCh <- dispatchResult{AgentName: agentName, Text: strings.TrimSpace(text), Err: runErr}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	failed := false
	ordered := make(map[string]dispatchResult, len(task.AgentNames))
	for result := range resultCh {
		ordered[result.AgentName] = result
		if result.Err != nil {
			failed = true
		}
	}

	for _, agentName := range task.AgentNames {
		result := ordered[agentName]
		item := SharedTaskResult{AgentName: agentName, Text: result.Text}
		if result.Err != nil {
			item.Error = result.Err.Error()
			resultTextParts = append(resultTextParts, fmt.Sprintf("## %s\nError: %s", agentName, result.Err))
		} else {
			text := result.Text
			if text == "" {
				text = "No response returned."
			}
			resultTextParts = append(resultTextParts, fmt.Sprintf("## %s\n%s", agentName, text))
		}
		results = append(results, item)
	}

	now := time.Now()
	m.queueMu.Lock()
	stored := m.sharedTasks[task.ID]
	if stored != nil {
		stored.Results = results
		stored.ResultText = strings.Join(resultTextParts, "\n\n")
		stored.FinishedAt = &now
		if failed {
			stored.Status = SharedTaskStatusFailed
		} else {
			stored.Status = SharedTaskStatusCompleted
		}
		_ = m.store.SaveSharedTask(stored)
	}
	m.queueMu.Unlock()

	if failed {
		m.failAsyncTask(task.ID, task.CaptainName, errors.New(strings.Join(resultTextParts, "\n\n")))
		return
	}
	m.completeAsyncTask(task.ID, strings.Join(resultTextParts, "\n\n"), task.CaptainName)
}

func (m *TeamManager) forwardRuntimeEvents(taskID string, events <-chan *Event) (string, error) {
	var finalText string
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
		case EventTypeError:
			msg := strings.TrimSpace(runtimeEvt.Content)
			if msg == "" {
				msg = "agent execution failed"
			}
			return finalText, errors.New(msg)
		}
	}
	return finalText, nil
}

func (m *TeamManager) completeAsyncTask(taskID, finalText, agentName string) {
	if strings.EqualFold(strings.TrimSpace(agentName), ArchivistAgentName) {
		if displayText, verifierPrompt, ok := parseArchivistVerifierEscalation(finalText); ok {
			finalText = displayText
			if strings.TrimSpace(verifierPrompt) != "" {
				_, _ = m.SubmitAgentTask(context.Background(), m.taskSessionID(taskID), VerifierAgentName, verifierPrompt)
			}
		}
	}
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

func (m *TeamManager) taskSessionID(taskID string) string {
	m.taskMu.RLock()
	defer m.taskMu.RUnlock()
	if task := m.asyncTasks[taskID]; task != nil {
		return strings.TrimSpace(task.SessionID)
	}
	return ""
}

func (m *TeamManager) failAsyncTask(taskID, agentName string, err error) {
	finishedAt := time.Now()
	task := m.updateAsyncTask(taskID, func(existing *AsyncTask) {
		existing.Status = AsyncTaskStatusFailed
		existing.Error = strings.TrimSpace(err.Error())
		existing.FinishedAt = &finishedAt
	})
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
