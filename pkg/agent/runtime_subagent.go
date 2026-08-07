package agent

import "strings"

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
