package agent

import (
	"strings"
	"time"

	taskpkg "github.com/liliang-cn/agent-go/v2/pkg/task"
)

func (s *Service) observeRunStream(session *Session, taskID, goal string, startedAt time.Time, upstream <-chan *Event) <-chan *Event {
	if upstream == nil {
		return upstream
	}
	out := make(chan *Event, 64)
	go func() {
		defer close(out)
		for evt := range upstream {
			if evt != nil {
				s.persistRunTaskEvent(session, taskID, evt)
				switch evt.Type {
				case EventTypeComplete:
					s.persistRunTaskState(session, taskID, taskRunStateOptions{
						status:     taskpkg.StatusCompleted,
						input:      goal,
						output:     strings.TrimSpace(evt.Content),
						createdAt:  startedAt,
						finishedAt: evt.Timestamp,
					})
				case EventTypeBlocked:
					s.persistRunTaskState(session, taskID, taskRunStateOptions{
						status:      taskpkg.StatusBlocked,
						input:       goal,
						output:      strings.TrimSpace(evt.Content),
						errorText:   strings.TrimSpace(evt.Content),
						createdAt:   startedAt,
						finishedAt:  evt.Timestamp,
						appendError: true,
					})
				case EventTypeError:
					s.persistRunTaskState(session, taskID, taskRunStateOptions{
						status:      taskpkg.StatusFailed,
						input:       goal,
						errorText:   strings.TrimSpace(evt.Content),
						createdAt:   startedAt,
						finishedAt:  evt.Timestamp,
						appendError: true,
					})
				}
			}
			out <- evt
		}
	}()
	return out
}
