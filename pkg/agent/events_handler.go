package agent

import (
	"context"
	"sync"
)

// EventHandler is a callback function for handling events
type EventHandler func(event *Event)

// EventHandlers holds callbacks for different event types
type EventHandlers struct {
	mu sync.RWMutex

	// OnThinking is called when the agent is thinking/processing
	OnThinking EventHandler

	// OnPartial is called when partial content is received (streaming)
	OnPartial EventHandler

	// OnToolCall is called when agent requests tool execution
	OnToolCall EventHandler

	// OnToolResult is called when tool execution completes
	OnToolResult EventHandler

	// OnStateUpdate is called when session state is updated
	OnStateUpdate EventHandler

	// OnHandoff is called when agent hands off to another agent
	OnHandoff EventHandler

	// OnStart is called when workflow starts
	OnStart EventHandler

	// OnComplete is called when workflow completes successfully
	OnComplete EventHandler

	// OnError is called when an error occurs
	OnError EventHandler

	// OnDebug is called when debug information is available
	OnDebug EventHandler

	// OnAnalytics is called for structured analytics events
	OnAnalytics EventHandler

	// OnAny is called for all events (catch-all)
	OnAny EventHandler
}

// SetHandler sets a handler for a specific event type
func (h *EventHandlers) SetHandler(eventType EventType, handler EventHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch eventType {
	case EventTypeThinking:
		h.OnThinking = handler
	case EventTypePartial:
		h.OnPartial = handler
	case EventTypeToolCall:
		h.OnToolCall = handler
	case EventTypeToolResult:
		h.OnToolResult = handler
	case EventTypeStateUpdate:
		h.OnStateUpdate = handler
	case EventTypeHandoff:
		h.OnHandoff = handler
	case EventTypeStart:
		h.OnStart = handler
	case EventTypeComplete:
		h.OnComplete = handler
	case EventTypeError:
		h.OnError = handler
	case EventTypeDebug:
		h.OnDebug = handler
	case EventTypeAnalytics:
		h.OnAnalytics = handler
	}
}

// Handle processes an event by calling the appropriate handler
func (h *EventHandlers) Handle(event *Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Call catch-all first
	if h.OnAny != nil {
		h.OnAny(event)
	}

	switch event.Type {
	case EventTypeThinking:
		if h.OnThinking != nil {
			h.OnThinking(event)
		}
	case EventTypePartial:
		if h.OnPartial != nil {
			h.OnPartial(event)
		}
	case EventTypeToolCall:
		if h.OnToolCall != nil {
			h.OnToolCall(event)
		}
	case EventTypeToolResult:
		if h.OnToolResult != nil {
			h.OnToolResult(event)
		}
	case EventTypeStateUpdate:
		if h.OnStateUpdate != nil {
			h.OnStateUpdate(event)
		}
	case EventTypeHandoff:
		if h.OnHandoff != nil {
			h.OnHandoff(event)
		}
	case EventTypeStart:
		if h.OnStart != nil {
			h.OnStart(event)
		}
	case EventTypeComplete:
		if h.OnComplete != nil {
			h.OnComplete(event)
		}
	case EventTypeError:
		if h.OnError != nil {
			h.OnError(event)
		}
	case EventTypeDebug:
		if h.OnDebug != nil {
			h.OnDebug(event)
		}
	case EventTypeAnalytics:
		if h.OnAnalytics != nil {
			h.OnAnalytics(event)
		}
	}
}

// NewEventHandlers creates a new EventHandlers with optional initial handlers
func NewEventHandlers() *EventHandlers {
	return &EventHandlers{}
}

// ============================================================
// Event Handler Builder - Fluent API
// ============================================================

// EventHandlerBuilder provides a fluent API for building event handlers
type EventHandlerBuilder struct {
	handlers *EventHandlers
}

// NewEventHandlerBuilder creates a new builder
func NewEventHandlerBuilder() *EventHandlerBuilder {
	return &EventHandlerBuilder{
		handlers: NewEventHandlers(),
	}
}

// OnThinking sets the thinking handler with simple string callback
func (b *EventHandlerBuilder) OnThinking(handler func(string)) *EventHandlerBuilder {
	if handler != nil {
		b.handlers.OnThinking = func(e *Event) { handler(e.Content) }
	}
	return b
}

// OnPartial sets the partial content handler with simple string callback
func (b *EventHandlerBuilder) OnPartial(handler func(string)) *EventHandlerBuilder {
	if handler != nil {
		b.handlers.OnPartial = func(e *Event) { handler(e.Content) }
	}
	return b
}

// OnToolCall sets the tool call handler with name and args callback
func (b *EventHandlerBuilder) OnToolCall(handler func(name string, args map[string]interface{})) *EventHandlerBuilder {
	if handler != nil {
		b.handlers.OnToolCall = func(e *Event) { handler(e.ToolName, e.ToolArgs) }
	}
	return b
}

// OnToolResult sets the tool result handler with name and result callback
func (b *EventHandlerBuilder) OnToolResult(handler func(name string, result any)) *EventHandlerBuilder {
	if handler != nil {
		b.handlers.OnToolResult = func(e *Event) { handler(e.ToolName, e.ToolResult) }
	}
	return b
}

// OnStateUpdate sets the state update handler
func (b *EventHandlerBuilder) OnStateUpdate(handler EventHandler) *EventHandlerBuilder {
	b.handlers.OnStateUpdate = handler
	return b
}

// OnHandoff sets the handoff handler
func (b *EventHandlerBuilder) OnHandoff(handler EventHandler) *EventHandlerBuilder {
	b.handlers.OnHandoff = handler
	return b
}

// OnStart sets the start handler
func (b *EventHandlerBuilder) OnStart(handler EventHandler) *EventHandlerBuilder {
	b.handlers.OnStart = handler
	return b
}

// OnComplete sets the complete handler with simple string callback
func (b *EventHandlerBuilder) OnComplete(handler func(string)) *EventHandlerBuilder {
	if handler != nil {
		b.handlers.OnComplete = func(e *Event) { handler(e.Content) }
	}
	return b
}

// OnError sets the error handler with simple string callback
func (b *EventHandlerBuilder) OnError(handler func(string)) *EventHandlerBuilder {
	if handler != nil {
		b.handlers.OnError = func(e *Event) { handler(e.Content) }
	}
	return b
}

// OnDebug sets the debug handler
func (b *EventHandlerBuilder) OnDebug(handler EventHandler) *EventHandlerBuilder {
	b.handlers.OnDebug = handler
	return b
}

// OnAny sets a catch-all handler for all events
func (b *EventHandlerBuilder) OnAny(handler EventHandler) *EventHandlerBuilder {
	b.handlers.OnAny = handler
	return b
}

// Build returns the configured EventHandlers
func (b *EventHandlerBuilder) Build() *EventHandlers {
	return b.handlers
}

// ============================================================
// Event Processor - Processes event channel with handlers
// ============================================================

// EventProcessor processes events from a channel
type EventProcessor struct {
	handlers *EventHandlers
	done     chan struct{}
	result   *Event // Final result
	err      error  // Final error
}

// Process starts processing events from the channel
func (p *EventProcessor) Process(ctx context.Context, eventChan <-chan *Event) {
	defer close(p.done)

	for {
		select {
		case <-ctx.Done():
			p.err = ctx.Err()
			return
		case event, ok := <-eventChan:
			if !ok {
				return
			}
			p.handlers.Handle(event)

			// Track final result/error
			switch event.Type {
			case EventTypeComplete:
				p.result = event
			case EventTypeError:
				p.err = &EventError{Event: event}
			}
		}
	}
}

// Result returns the final result
func (p *EventProcessor) Result() *Event {
	return p.result
}

// Error returns the final error
func (p *EventProcessor) Error() error {
	return p.err
}

// Done returns the done channel
func (p *EventProcessor) Done() <-chan struct{} {
	return p.done
}

// NewEventProcessor creates a new event processor
func NewEventProcessor(handlers *EventHandlers) *EventProcessor {
	return &EventProcessor{
		handlers: handlers,
		done:     make(chan struct{}),
	}
}

// ============================================================
// Convenience Functions
// ============================================================

// SimpleEventHandler is a simple function type for handlers
type SimpleEventHandler func(content string)

// ToHandler converts a SimpleEventHandler to EventHandler
func (s SimpleEventHandler) ToHandler() EventHandler {
	return func(e *Event) {
		s(e.Content)
	}
}

// HandleEventsSimple handles events with simple string-based callbacks
func HandleEventsSimple(
	ctx context.Context,
	eventChan <-chan *Event,
	onThinking func(string),
	onToolCall func(name string, args map[string]interface{}),
	onToolResult func(name string, result interface{}),
	onComplete func(result string),
	onError func(err string),
) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-eventChan:
			if !ok {
				return
			}

			switch event.Type {
			case EventTypeThinking:
				if onThinking != nil {
					onThinking(event.Content)
				}
			case EventTypeToolCall:
				if onToolCall != nil {
					onToolCall(event.ToolName, event.ToolArgs)
				}
			case EventTypeToolResult:
				if onToolResult != nil {
					onToolResult(event.ToolName, event.ToolResult)
				}
			case EventTypeComplete:
				if onComplete != nil {
					onComplete(event.Content)
				}
			case EventTypeError:
				if onError != nil {
					onError(event.Content)
				}
			}
		}
	}
}

// EventError wraps an error event
type EventError struct {
	Event *Event
}

func (e *EventError) Error() string {
	return e.Event.Content
}

// Unwrap returns the underlying event for errors.Is() support
func (e *EventError) Unwrap() error {
	return nil
}
