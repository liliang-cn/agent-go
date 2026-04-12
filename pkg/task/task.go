package task

import (
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type Kind string

const (
	KindAgent     Kind = "agent"
	KindTeam      Kind = "team"
	KindScheduler Kind = "scheduler"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusYielded   Status = "yielded"
	StatusResumable Status = "resumable"
	StatusResuming  Status = "resuming"
	StatusCompleted Status = "completed"
	StatusBlocked   Status = "blocked"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type TerminalState string

const (
	TerminalCompleted TerminalState = "completed"
	TerminalBlocked   TerminalState = "blocked"
	TerminalFailed    TerminalState = "failed"
	TerminalYielded   TerminalState = "yielded"
)

type QueueClass string

const (
	QueueClassTask      QueueClass = "task"
	QueueClassMicrotask QueueClass = "microtask"
)

type AwaitingState struct {
	Type       string    `json:"type,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	AgentName  string    `json:"agent_name,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Since      time.Time `json:"since,omitempty"`
}

// Frame is one LLM/tool turn inside a task execution. A task can span many
// frames while still being one logical function invocation.
type Frame struct {
	SessionID string         `json:"session_id"`
	Message   domain.Message `json:"message"`
}

type Event struct {
	ID          string         `json:"id"`
	TaskID      string         `json:"task_id"`
	SessionID   string         `json:"session_id,omitempty"`
	Kind        Kind           `json:"kind"`
	Status      Status         `json:"status"`
	Type        string         `json:"type"`
	TeamID      string         `json:"team_id,omitempty"`
	TeamName    string         `json:"team_name,omitempty"`
	CaptainName string         `json:"captain_name,omitempty"`
	AgentName   string         `json:"agent_name,omitempty"`
	Message     string         `json:"message,omitempty"`
	Runtime     map[string]any `json:"runtime,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
}

// Task is the first-class execution unit in AgentGo.
//
// Team is the process, Agent is the thread, and Task is the function
// invocation/activation frame. One Task may include many LLM/tool frames.
type Task struct {
	ID               string         `json:"id"`
	Kind             Kind           `json:"kind"`
	Status           Status         `json:"status"`
	SessionID        string         `json:"session_id,omitempty"`
	RuntimeSessionID string         `json:"runtime_session_id,omitempty"`
	ParentTaskID     string         `json:"parent_task_id,omitempty"`
	ContinuationID   string         `json:"continuation_id,omitempty"`
	QueueClass       QueueClass     `json:"queue_class,omitempty"`
	Awaiting         *AwaitingState `json:"awaiting,omitempty"`
	TeamID           string         `json:"team_id,omitempty"`
	TeamName         string         `json:"team_name,omitempty"`
	AgentName        string         `json:"agent_name,omitempty"`
	AgentNames       []string       `json:"agent_names,omitempty"`
	Input            string         `json:"input,omitempty"`
	Output           string         `json:"output,omitempty"`
	Error            string         `json:"error,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	StartedAt        *time.Time     `json:"started_at,omitempty"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	Frames           []Frame        `json:"frames,omitempty"`
	Events           []Event        `json:"events,omitempty"`
	Source           string         `json:"source"`
	SourceID         string         `json:"source_id"`
}
