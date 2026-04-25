package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

type taskRunStateOptions struct {
	status      taskpkg.Status
	input       string
	output      string
	errorText   string
	createdAt   time.Time
	finishedAt  time.Time
	appendError bool
}

func (s *Service) persistRunTaskState(session *Session, taskID string, opts taskRunStateOptions) {
	if s == nil || s.store == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	parentTaskID := ""
	if session != nil {
		if raw, ok := session.GetContext("runtime.parent_task_id"); ok {
			if s, ok := raw.(string); ok {
				parentTaskID = strings.TrimSpace(s)
			}
		}
	}
	task, err := s.store.GetTask(taskID)
	if err != nil || task == nil {
		task = &taskpkg.Task{
			ID:               strings.TrimSpace(taskID),
			Kind:             taskpkg.KindAgent,
			SessionID:        sessionIDOrEmpty(session),
			RuntimeSessionID: sessionIDOrEmpty(session),
			ParentTaskID:     parentTaskID,
			CreatedAt:        firstNonZeroTime(opts.createdAt, time.Now()),
			Source:           "run",
			SourceID:         sessionIDOrEmpty(session),
		}
	}
	if task.ParentTaskID == "" && parentTaskID != "" {
		task.ParentTaskID = parentTaskID
	}
	task.Status = opts.status
	if text := strings.TrimSpace(opts.input); text != "" {
		task.Input = text
	}
	if text := strings.TrimSpace(opts.output); text != "" {
		task.Output = text
	}
	if text := strings.TrimSpace(opts.errorText); text != "" {
		task.Error = text
	}
	if !opts.finishedAt.IsZero() {
		finished := opts.finishedAt
		task.FinishedAt = &finished
	}
	if opts.appendError && session != nil && strings.TrimSpace(opts.errorText) != "" {
		session.AddMessage(withTaskID(domain.Message{
			Role:    "assistant",
			Content: "Execution failed: " + strings.TrimSpace(opts.errorText),
		}, taskID))
	}
	_ = s.store.SaveTask(task)
	if session != nil && opts.appendError {
		_ = s.store.SaveSession(session)
	}
}

func (s *Service) persistRunTaskEvent(session *Session, taskID string, evt *Event) {
	if s == nil || s.store == nil || strings.TrimSpace(taskID) == "" || evt == nil {
		return
	}
	if strings.TrimSpace(evt.ID) == "" {
		evt.ID = uuid.NewString()
	}
	parentTaskID := ""
	if session != nil {
		if raw, ok := session.GetContext("runtime.parent_task_id"); ok {
			if s, ok := raw.(string); ok {
				parentTaskID = strings.TrimSpace(s)
			}
		}
	}
	task, err := s.store.GetTask(taskID)
	if err != nil || task == nil {
		s.persistRunTaskState(session, taskID, taskRunStateOptions{
			status:    taskpkg.StatusRunning,
			createdAt: evt.Timestamp,
		})
		task, _ = s.store.GetTask(taskID)
	}
	if task == nil {
		return
	}

	status := task.Status
	switch evt.Type {
	case EventTypeComplete:
		status = taskpkg.StatusCompleted
	case EventTypeBlocked:
		status = taskpkg.StatusBlocked
	case EventTypeError:
		status = taskpkg.StatusFailed
	}

	runtime := &taskpkg.EventRuntime{
		ToolName:   evt.ToolName,
		ToolArgs:   evt.ToolArgs,
		ToolResult: evt.ToolResult,
		DurationMs: evt.DurationMs,
		Round:      evt.Round,
		TokensUsed: evt.TokensUsed,
		Duplicate:  evt.Duplicate,
	}

	// Detect duplicate tool calls (same tool + same args seen before).
	if evt.Type == EventTypeToolCall && evt.ToolName != "" {
		key := fmt.Sprintf("%s:%v", evt.ToolName, evt.ToolArgs)
		for i := range task.Events {
			existing := &task.Events[i]
			if existing.Type != string(EventTypeToolCall) || existing.Runtime == nil {
				continue
			}
			if fmt.Sprintf("%s:%v", existing.Runtime.ToolName, existing.Runtime.ToolArgs) == key {
				runtime.Duplicate = true
				break
			}
		}
	}

	task.Events = append(task.Events, taskpkg.Event{
		ID:         evt.ID,
		TaskID:     strings.TrimSpace(taskID),
		SessionID:  sessionIDOrEmpty(session),
		Kind:       taskpkg.KindAgent,
		Status:     status,
		Type:       string(evt.Type),
		AgentName:  strings.TrimSpace(evt.AgentName),
		Message:    strings.TrimSpace(evt.Content),
		DurationMs: evt.DurationMs,
		Runtime:    runtime,
		Timestamp:  firstNonZeroTime(evt.Timestamp, time.Now()),
	})
	task.Status = status
	if evt.Type == EventTypeComplete || evt.Type == EventTypeBlocked {
		task.Output = strings.TrimSpace(evt.Content)
	}
	if evt.Type == EventTypeError {
		task.Error = strings.TrimSpace(evt.Content)
	}
	if task.ParentTaskID == "" && parentTaskID != "" {
		task.ParentTaskID = parentTaskID
	}
	_ = s.store.SaveTask(task)
}

func (s *Service) persistRunTaskStats(session *Session, taskID string, metrics *executionMetrics) {
	if s == nil || s.store == nil || metrics == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	task, err := s.store.GetTask(taskID)
	if err != nil || task == nil {
		return
	}
	stats := &taskpkg.TaskStats{
		Rounds:      metrics.rounds,
		TotalTokens: metrics.estimatedTokens,
		ToolCalls:   metrics.toolCalls,
		ToolsUsed:   metrics.toolsUsed,
		DurationMs:  metrics.totalDurationMs,
	}
	for _, rs := range metrics.roundStats {
		stats.RoundBreakdown = append(stats.RoundBreakdown, taskpkg.RoundStats{
			Round:      rs.round,
			TokensUsed: rs.tokens,
			ToolCalls:  rs.toolCalls,
			LLMMs:      rs.llmMs,
			ToolMs:     rs.toolMs,
			DurationMs: rs.durationMs,
		})
	}
	task.Stats = stats
	_ = s.store.SaveTask(task)
}

func (s *Service) persistRunMessages(session *Session, messages ...domain.Message) {
	if s == nil || s.store == nil || session == nil || len(messages) == 0 {
		return
	}
	for _, message := range messages {
		session.AddMessage(message)
	}
	_ = s.store.SaveSession(session)
}

func appendNewMessagesToSession(session *Session, previousLen int, messages []domain.Message) []domain.Message {
	if session == nil {
		return nil
	}
	if previousLen < 0 {
		previousLen = 0
	}
	if previousLen >= len(messages) {
		return nil
	}
	delta := append([]domain.Message(nil), messages[previousLen:]...)
	for _, message := range delta {
		session.AddMessage(message)
	}
	return delta
}
