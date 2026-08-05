package agent

import (
	"time"

	"github.com/google/uuid"
)

func (sa *SubAgent) Events() <-chan *Event {
	return sa.events
}

func (sa *SubAgent) emitEvent(evt *Event) {
	if evt == nil || sa.events == nil {
		return
	}
	select {
	case sa.events <- evt:
	default:
	}
}

func (sa *SubAgent) emitStart(content string) {
	sa.emitSimpleEvent(EventTypeStart, content)
}

func (sa *SubAgent) emitComplete(content string) {
	sa.emitSimpleEvent(EventTypeComplete, content)
}

func (sa *SubAgent) emitError(content string) {
	sa.emitSimpleEvent(EventTypeError, content)
}

func (sa *SubAgent) emitSimpleEvent(eventType EventType, content string) {
	sa.emitEvent(&Event{
		ID:        uuid.NewString(),
		Type:      eventType,
		AgentID:   sa.config.Agent.ID(),
		AgentName: sa.config.Agent.Name(),
		Content:   content,
		Timestamp: time.Now(),
	})
}
