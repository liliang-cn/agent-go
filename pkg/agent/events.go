package agent

import (
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// EventType defines the type of event in the runtime loop
type EventType string

const (
	TurnStagePreparingContext = "preparing_context"
	TurnStageAwaitingModel    = "awaiting_model"
	TurnStageHandlingTools    = "handling_tools"
	TurnStageAwaitingAnswer   = "awaiting_answer"
	TurnStageCompleted        = "completed"
)

const (
	// Workflow Events
	EventTypeStart    EventType = "workflow_start"
	EventTypeComplete EventType = "workflow_complete"
	EventTypeError    EventType = "workflow_error"

	// Thinking & Streaming
	EventTypeThinking EventType = "thinking" // Agent is processing
	EventTypePartial  EventType = "partial"  // Streaming text output
	EventTypeTombstone EventType = "tombstone" // Request to remove/clear partial content (e.g. after interruption)

	// Tool Execution
	EventTypeToolCall   EventType = "tool_call"   // Agent requests tool execution
	EventTypeToolResult EventType = "tool_result" // Runner returns tool result

	// State Management
	EventTypeStateUpdate EventType = "state_update" // Request to update session state

	// Handoff
	EventTypeHandoff EventType = "handoff" // Transferring to another agent

	// Stop Hooks
	EventTypeStop         EventType = "stop"          // Stop hook prevented continuation
	EventTypeStopComplete EventType = "stop_complete"  // Stop hook executed successfully

	// Debug (prompts/responses, emitted when debug=true)
	EventTypeDebug EventType = "debug"

	// Profiling & Analytics
	EventTypeCheckpoint EventType = "checkpoint" // Profiling checkpoint event
	EventTypeAnalytics  EventType = "analytics"  // Structured analytics event
)

// Analytics event names
const (
	AnalyticsAutocompactTriggered  = "tengu_autocompact_triggered"
	AnalyticsLLMLatency           = "tengu_llm_latency"
	AnalyticsToolExecutionLatency = "tengu_tool_execution_latency"
	AnalyticsQueryCompleted       = "tengu_query_completed"
	AnalyticsTokenBudgetExceeded  = "tengu_token_budget_exceeded"
	AnalyticsRoundCompleted       = "tengu_round_completed"
)

// AnalyticsEvent represents structured analytics data
type AnalyticsEvent struct {
	Name string                 `json:"name"`
	Data map[string]interface{} `json:"data"`
}

// Event represents a discrete occurrence in the agent execution loop
type Event struct {
	ID        string    `json:"id"`
	Type      EventType `json:"type"`
	AgentID   string    `json:"agent_id,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	Content   string    `json:"content,omitempty"` // For text/thinking

	// Tool data
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolArgs   map[string]interface{} `json:"tool_args,omitempty"`
	ToolResult interface{}            `json:"tool_result,omitempty"`

	// RAG sources (for workflow_complete event)
	Sources []domain.Chunk `json:"sources,omitempty"`

	// State data
	StateDelta map[string]interface{} `json:"state_delta,omitempty"`

	// Debug data (EventTypeDebug only)
	Round     int    `json:"round,omitempty"`
	DebugType string `json:"debug_type,omitempty"` // "prompt" or "response"

	// Checkpoint data (EventTypeCheckpoint only)
	CheckpointName    string        `json:"checkpoint_name,omitempty"`
	CheckpointStart   time.Time     `json:"checkpoint_start,omitempty"`
	CheckpointEnd     time.Time     `json:"checkpoint_end,omitempty"`
	CheckpointDuration time.Duration `json:"checkpoint_duration,omitempty"`

	// Analytics data (EventTypeAnalytics only)
	AnalyticsEvent *AnalyticsEvent `json:"analytics_event,omitempty"`

	Timestamp time.Time `json:"timestamp"`
}

// NewEvent creates a basic event
func NewEvent(evtType EventType, agent *Agent) *Event {
	agentName := "System"
	agentID := "system"
	if agent != nil {
		agentName = agent.Name()
		agentID = agent.ID()
	}

	return &Event{
		Type:      evtType,
		AgentName: agentName,
		AgentID:   agentID,
		Timestamp: time.Now(),
	}
}

// NewAnalyticsEvent creates a new analytics event
func NewAnalyticsEvent(name string, data map[string]interface{}) *Event {
	return &Event{
		Type:      EventTypeAnalytics,
		AnalyticsEvent: &AnalyticsEvent{
			Name: name,
			Data: data,
		},
		Timestamp: time.Now(),
	}
}
