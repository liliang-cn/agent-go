package agent

import (
	"fmt"
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

func (sa *SubAgent) emitThinking(content string) {
	sa.emitSimpleEvent(EventTypeThinking, content)
}

func (sa *SubAgent) emitPartial(content string) {
	sa.emitSimpleEvent(EventTypePartial, content)
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

func (sa *SubAgent) emitToolCall(name string, args map[string]interface{}) {
	sa.emitEvent(&Event{
		ID:        uuid.NewString(),
		Type:      EventTypeToolCall,
		AgentID:   sa.config.Agent.ID(),
		AgentName: sa.config.Agent.Name(),
		ToolName:  name,
		ToolArgs:  args,
		Timestamp: time.Now(),
	})
}

func (sa *SubAgent) emitToolResult(name string, result interface{}, err error) {
	evt := &Event{
		ID:         uuid.NewString(),
		Type:       EventTypeToolResult,
		AgentID:    sa.config.Agent.ID(),
		AgentName:  sa.config.Agent.Name(),
		ToolName:   name,
		ToolResult: result,
		Timestamp:  time.Now(),
	}
	if err != nil {
		evt.Content = err.Error()
	}
	sa.emitEvent(evt)
}

func (sa *SubAgent) emitDebug(round int, debugType, content string) {
	sa.emitEvent(&Event{
		ID:        uuid.NewString(),
		Type:      EventTypeDebug,
		AgentID:   sa.config.Agent.ID(),
		AgentName: sa.config.Agent.Name(),
		Round:     round,
		DebugType: debugType,
		Content:   content,
		Timestamp: time.Now(),
	})
}

func (sa *SubAgent) emitLoopState(state *queryLoopState) {
	if state == nil {
		return
	}
	stateDelta := map[string]interface{}{
		"turn_stage":        state.Stage,
		"loop_transition":   state.LoopTransition,
		"transition_reason": state.TransitionReason,
		"round":             state.CurrentRound,
		"tool_call_count":   state.PendingToolCount,
	}
	if state.Intent != nil {
		stateDelta["intent_type"] = state.Intent.IntentType
		stateDelta["preferred_agent"] = state.Intent.PreferredAgent
		stateDelta["requires_tools"] = state.Intent.RequiresTools
		stateDelta["transition"] = state.Intent.Transition
	}
	sa.emitEvent(&Event{
		ID:         uuid.NewString(),
		Type:       EventTypeStateUpdate,
		AgentID:    sa.config.Agent.ID(),
		AgentName:  sa.config.Agent.Name(),
		Content:    fmt.Sprintf("Turn %d: %s", state.CurrentRound, state.TransitionReason),
		StateDelta: stateDelta,
		Timestamp:  time.Now(),
	})
}
