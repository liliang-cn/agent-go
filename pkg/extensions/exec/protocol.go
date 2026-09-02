package exec

import (
	"encoding/json"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// ProtocolVersion is the wire version this package speaks. Both sides state
// it in the handshake and a mismatch fails the build, because a plugin that
// guesses at the framing is a plugin that fails closed on every request.
const ProtocolVersion = 1

// Request types. Every request is one JSON object on one line, carrying a
// monotonically increasing "id"; the reply is one JSON object on one line
// echoing that id.
const (
	typeHello      = "hello"
	typeContext    = "context"
	typeBeforeTool = "before_tool"
	typeAfterTool  = "after_tool"
	typeLint       = "lint"
	typeRunStart   = "run_start"
	typeRunEnd     = "run_end"
	typeShutdown   = "shutdown"
)

// Capabilities a plugin may declare in its handshake. Anything it does not
// declare is never sent to it.
const (
	CapContext    = "context"
	CapBeforeTool = "before_tool"
	CapAfterTool  = "after_tool"
	CapLint       = "lint"
	CapRunStart   = "run_start"
	CapRunEnd     = "run_end"
)

// Capabilities is every capability name this version understands.
var Capabilities = []string{CapContext, CapBeforeTool, CapAfterTool, CapLint, CapRunStart, CapRunEnd}

func knownCapability(name string) bool {
	for _, c := range Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// request is one line written to the plugin's stdin. Exactly one payload
// field is set, chosen by Type; each payload mirrors the Go type of the seam
// it serves, in snake_case.
type request struct {
	ID       uint64 `json:"id"`
	Type     string `json:"type"`
	Protocol int    `json:"protocol,omitempty"`
	Name     string `json:"name,omitempty"`

	Context *contextInput   `json:"context,omitempty"`
	Call    *toolCallInfo   `json:"call,omitempty"`
	Result  *toolResultInfo `json:"result,omitempty"`
	Lint    *lintInput      `json:"lint,omitempty"`
	Run     *runInfo        `json:"run,omitempty"`
	Outcome *runOutcome     `json:"outcome,omitempty"`
}

// reply is one line read from the plugin's stdout. Fields not relevant to the
// request type are ignored; a non-empty Error is an error to the framework
// whatever the type was.
type reply struct {
	ID    uint64 `json:"id"`
	Type  string `json:"type"`
	Error string `json:"error"`

	// hello
	Protocol     int      `json:"protocol"`
	Capabilities []string `json:"capabilities"`

	// context
	Messages []replyMessage `json:"messages"`

	// before_tool
	Args  map[string]interface{} `json:"args"`
	Block string                 `json:"block"`

	// after_tool
	Result   json.RawMessage `json:"result"`
	Replaced bool            `json:"replaced"`

	// lint
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}

type replyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// contextInput mirrors agent.ContextInput.
type contextInput struct {
	Goal      string `json:"goal"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
}

// toolCallInfo mirrors agent.ToolCallInfo.
type toolCallInfo struct {
	Name      string                 `json:"name"`
	Args      map[string]interface{} `json:"args"`
	SessionID string                 `json:"session_id"`
	AgentID   string                 `json:"agent_id"`
}

// toolResultInfo mirrors agent.ToolResultInfo. Err travels as a string
// because an error has no wire form; empty means the tool succeeded.
type toolResultInfo struct {
	Name      string                 `json:"name"`
	Args      map[string]interface{} `json:"args"`
	Result    interface{}            `json:"result"`
	Error     string                 `json:"error"`
	SessionID string                 `json:"session_id"`
	AgentID   string                 `json:"agent_id"`
}

// lintInput mirrors agent.LintContext plus the text under inspection.
type lintInput struct {
	Text             string                         `json:"text"`
	AgentName        string                         `json:"agent_name"`
	TaskID           string                         `json:"task_id"`
	SessionID        string                         `json:"session_id"`
	TurnIndex        int                            `json:"turn_index"`
	Goal             string                         `json:"goal"`
	ToolCalls        []string                       `json:"tool_calls"`
	AvailableTools   []string                       `json:"available_tools"`
	Deliverables     []agent.DeliverableRequirement `json:"deliverables,omitempty"`
	RequestedActions []agent.RequestedAction        `json:"requested_actions,omitempty"`
	Workspace        string                         `json:"workspace"`
	IsRetry          bool                           `json:"is_retry"`
	RetryCount       int                            `json:"retry_count"`
}

// runInfo mirrors agent.RunInfo.
type runInfo struct {
	Goal      string `json:"goal"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	TaskID    string `json:"task_id"`
}

// runOutcome mirrors agent.RunOutcome. Duration is milliseconds because a Go
// duration is nanoseconds and nothing outside Go reads that as a time.
type runOutcome struct {
	StopReason string `json:"stop_reason"`
	Text       string `json:"text"`
	Blocked    bool   `json:"blocked"`
	Cancelled  bool   `json:"cancelled"`
	DurationMS int64  `json:"duration_ms"`
}
