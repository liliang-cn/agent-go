package agent

import (
	"strings"
	"time"

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
	task, err := s.store.GetTask(taskID)
	if err != nil || task == nil {
		task = &taskpkg.Task{
			ID:               strings.TrimSpace(taskID),
			Kind:             taskpkg.KindAgent,
			SessionID:        sessionIDOrEmpty(session),
			RuntimeSessionID: sessionIDOrEmpty(session),
			CreatedAt:        firstNonZeroTime(opts.createdAt, time.Now()),
			Source:           "run",
			SourceID:         sessionIDOrEmpty(session),
		}
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
