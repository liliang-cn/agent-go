package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
	taskpkg "github.com/liliang-cn/agent-go/v3/pkg/task"
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
	// One atomic read-modify-write. The runtime goroutine writes Frames onto
	// this same row; a plain GetTask/SaveTask pair here would read before it
	// and write back over it.
	_ = s.store.updateTask(taskID, func(task *UnifiedTask) *UnifiedTask {
		if task == nil {
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
		return task
	})
	if opts.appendError && session != nil && strings.TrimSpace(opts.errorText) != "" {
		session.AddMessage(withTaskID(domain.Message{
			Role:    "assistant",
			Content: "Execution failed: " + strings.TrimSpace(opts.errorText),
		}, taskID))
	}
	if session != nil && opts.appendError {
		_ = s.store.SaveSession(session)
	}
}

func (s *Service) persistRunTaskEvent(session *Session, taskID string, evt *Event) {
	if s == nil || s.store == nil || strings.TrimSpace(taskID) == "" || evt == nil {
		return
	}
	// A streamed fragment — one token of an answer or of its reasoning — is
	// not a record of anything: the answer itself lands with the terminal
	// event. Writing each one rewrote the whole task row, so a turn cost the
	// square of its length: on a live hive with a long-lived task row, a
	// three-word greeting took 85s to stream, about two tokens a second.
	if isStreamFragment(evt.Type) {
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
	appendEvent := func(te *store.TaskEvents) {
		status := te.Status
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
			ToolResult: storedToolResult(evt.ToolResult),
			DurationMs: evt.DurationMs,
			Round:      evt.Round,
			TokensUsed: evt.TokensUsed,
			Duplicate:  evt.Duplicate,
		}

		// Detect duplicate tool calls (same tool + same args seen before).
		if evt.Type == EventTypeToolCall && evt.ToolName != "" {
			key := fmt.Sprintf("%s:%v", evt.ToolName, evt.ToolArgs)
			for i := range te.Events {
				existing := &te.Events[i]
				if existing.Type != string(EventTypeToolCall) || existing.Runtime == nil || existing.Runtime.ToolName != evt.ToolName {
					continue
				}
				if fmt.Sprintf("%s:%v", existing.Runtime.ToolName, existing.Runtime.ToolArgs) == key {
					runtime.Duplicate = true
					break
				}
			}
		}

		te.Events = append(te.Events, taskpkg.Event{
			ID:         evt.ID,
			TaskID:     strings.TrimSpace(taskID),
			SessionID:  sessionIDOrEmpty(session),
			Kind:       taskpkg.KindAgent,
			Status:     status,
			Type:       string(evt.Type),
			AgentName:  strings.TrimSpace(evt.AgentName),
			Message:    clipStored(strings.TrimSpace(evt.Content)),
			DurationMs: evt.DurationMs,
			Runtime:    runtime,
			Timestamp:  firstNonZeroTime(evt.Timestamp, time.Now()),
		})
		// A session's turns share one task, so its log grew for as long as the
		// conversation lasted and every event rewrote all of it. Keep the
		// recent end, as the in-memory task log does (appendTaskEvent).
		if n := len(te.Events); n > maxStoredTaskEvents {
			te.Events = append([]taskpkg.Event(nil), te.Events[n-maxStoredTaskEvents:]...)
		}
		te.Status = status
		if evt.Type == EventTypeComplete || evt.Type == EventTypeBlocked {
			te.Output = strings.TrimSpace(evt.Content)
		}
		if evt.Type == EventTypeError {
			te.Error = strings.TrimSpace(evt.Content)
		}
		if te.ParentTaskID == "" && parentTaskID != "" {
			te.ParentTaskID = parentTaskID
		}
	}

	// Atomic read-modify-write of the event columns only: the runtime
	// goroutine writes Frames onto the same row, and appending Events with a
	// separate GetTask/SaveTask pair silently dropped whichever side read
	// first. The first event of a task finds no row and creates it.
	if err := s.store.updateTaskEvents(taskID, appendEvent); errors.Is(err, errNoTaskRow) {
		s.persistRunTaskState(session, taskID, taskRunStateOptions{
			status:    taskpkg.StatusRunning,
			createdAt: evt.Timestamp,
		})
		_ = s.store.updateTaskEvents(taskID, appendEvent)
	}
}

// A task's stored event log keeps its most recent events, and what each one
// carries is clipped: the log says what happened, the session holds the full
// text.
const (
	maxStoredTaskEvents = 200
	maxStoredEventText  = 4000
)

func clipStored(s string) string {
	if len(s) <= maxStoredEventText {
		return s
	}
	cut := maxStoredEventText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// storedToolResult keeps a short result as it is and clips a long one to its
// text, so one big command output cannot make every later write of the log
// carry it again.
func storedToolResult(v interface{}) interface{} {
	switch r := v.(type) {
	case nil:
		return nil
	case string:
		return clipStored(r)
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) <= maxStoredEventText {
		return v
	}
	return clipStored(string(raw))
}

// isStreamFragment reports whether an event is a piece of a stream rather
// than a step of the run.
func isStreamFragment(t EventType) bool {
	return t == EventTypePartial || t == EventTypeThinking || t == EventTypeTombstone
}

func (s *Service) persistRunTaskStats(session *Session, taskID string, metrics *executionMetrics) {
	if s == nil || s.store == nil || metrics == nil || strings.TrimSpace(taskID) == "" {
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
	// Runs after the stream closes, but the runtime's own final SaveSession can
	// still be in flight, so this takes the lock like every other task write.
	_ = s.store.updateTask(taskID, func(task *UnifiedTask) *UnifiedTask {
		if task == nil {
			return nil
		}
		task.Stats = stats
		return task
	})
}
