package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func (r *Runtime) executeToolViaSubAgent(ctx context.Context, tc domain.ToolCall) (interface{}, error, bool) {
	subagentID := uuid.NewString()
	r.emit(EventTypeThinking, fmt.Sprintf("Delegating %s to SubAgent %s...", tc.Function.Name, subagentID[:8]))

	return r.svc.executeToolViaSubAgentWithEvents(ctx, r.currentAgent, r.session, tc, r.forwardSubAgentEvent, r.debugEnabled())
}

func (r *Runtime) forwardSubAgentEvent(evt *Event) {
	if evt == nil || r.eventChan == nil {
		return
	}

	forwarded := *evt
	switch forwarded.Type {
	case EventTypeStart:
		forwarded.Type = EventTypeStateUpdate
	case EventTypeComplete:
		forwarded.Type = EventTypeStateUpdate
		forwarded.Content = "Delegated step completed"
	case EventTypeError:
		forwarded.Type = EventTypeStateUpdate
		if strings.TrimSpace(forwarded.Content) == "" {
			forwarded.Content = "Delegated step failed"
		} else {
			forwarded.Content = "Delegated step failed: " + forwarded.Content
		}
	}

	r.eventChan <- &forwarded
}
