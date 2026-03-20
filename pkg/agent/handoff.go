package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// HandoffKind defines the type of handoff
type HandoffKind string

const (
	HandoffKindAgent  HandoffKind = "agent"  // Handoff to another agent
	HandoffKindTool   HandoffKind = "tool"   // Handoff to a specific tool
	HandoffKindManual HandoffKind = "manual" // Manual handoff with custom handler
)

// HandoffInputFilter is a function that filters/modifies input when handoff occurs
type HandoffInputFilter func(ctx context.Context, data HandoffInputData) (HandoffInputData, error)

// HandoffCallback is called when a handoff is invoked
type HandoffCallback func(ctx context.Context, data HandoffInputData) error

// HandoffInputData represents the data passed during a handoff
type HandoffInputData struct {
	Goal        string                 `json:"goal"`
	Messages    []HandoffMessage       `json:"messages,omitempty"`
	Context     map[string]interface{} `json:"context,omitempty"`
	SourceAgent string                 `json:"source_agent"`
	TargetAgent string                 `json:"target_agent"`
}

// HandoffMessage represents a message in the handoff conversation
// This is a simplified version of domain.Message for handoff purposes
type HandoffMessage struct {
	Role      string                 `json:"role"` // user, assistant, system, tool
	Content   string                 `json:"content"`
	ToolCalls []HandoffToolCall      `json:"tool_calls,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// HandoffToolCall represents a tool call in handoff
type HandoffToolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// Handoff represents a delegation from one agent to another
// Inspired by OpenAI Agents SDK handoff pattern
type Handoff struct {
	id                 string
	kind               HandoffKind
	targetAgent        *Agent
	toolName           string
	toolDescription    string
	onHandoff          HandoffCallback
	inputFilter        HandoffInputFilter
	inputTypeSchema    interface{}
	isEnabled          bool
	isEnabledFunc      func(ctx context.Context) bool
	nestHandoffHistory bool
	metadata           map[string]interface{}
}

// HandoffOption configures a Handoff
type HandoffOption func(*Handoff)

// WithHandoffToolName overrides the default tool name
func WithHandoffToolName(name string) HandoffOption {
	return func(h *Handoff) {
		h.toolName = name
	}
}

// WithHandoffToolDescription overrides the default tool description
func WithHandoffToolDescription(desc string) HandoffOption {
	return func(h *Handoff) {
		h.toolDescription = desc
	}
}

// WithHandoffCallback sets a callback invoked when handoff occurs
func WithHandoffCallback(callback HandoffCallback) HandoffOption {
	return func(h *Handoff) {
		h.onHandoff = callback
	}
}

// WithHandoffInputFilter sets a filter for input data during handoff
func WithHandoffInputFilter(filter HandoffInputFilter) HandoffOption {
	return func(h *Handoff) {
		h.inputFilter = filter
	}
}

// WithHandoffEnabled sets whether the handoff is enabled
func WithHandoffEnabled(enabled bool) HandoffOption {
	return func(h *Handoff) {
		h.isEnabled = enabled
		h.isEnabledFunc = nil
	}
}

// WithHandoffEnabledFunc sets a dynamic function to determine if handoff is enabled
func WithHandoffEnabledFunc(fn func(ctx context.Context) bool) HandoffOption {
	return func(h *Handoff) {
		h.isEnabledFunc = fn
		h.isEnabled = true
	}
}

// WithHandoffMetadata adds metadata to the handoff
func WithHandoffMetadata(key string, value interface{}) HandoffOption {
	return func(h *Handoff) {
		if h.metadata == nil {
			h.metadata = make(map[string]interface{})
		}
		h.metadata[key] = value
	}
}

// WithNestHandoffHistory sets whether to nest conversation history
func WithNestHandoffHistory(nest bool) HandoffOption {
	return func(h *Handoff) {
		h.nestHandoffHistory = nest
	}
}

// NewHandoff creates a new handoff to a target agent
func NewHandoff(target *Agent, opts ...HandoffOption) *Handoff {
	h := &Handoff{
		id:                 uuid.New().String(),
		kind:               HandoffKindAgent,
		targetAgent:        target,
		toolName:           DefaultToolName(target),
		toolDescription:    DefaultToolDescription(target),
		isEnabled:          true,
		nestHandoffHistory: true,
		metadata:           make(map[string]interface{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// DefaultToolName generates the default tool name for a handoff
func DefaultToolName(agent *Agent) string {
	return fmt.Sprintf("transfer_to_%s", normalizeToolName(agent.Name()))
}

// DefaultToolDescription generates the default tool description for a handoff
func DefaultToolDescription(agent *Agent) string {
	return fmt.Sprintf("Transfer control to %s agent", agent.Name())
}

// normalizeToolName converts a name to a valid tool name
func normalizeToolName(name string) string {
	result := ""
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			result += string(c)
		} else if c >= 'A' && c <= 'Z' {
			result += string(c + 32)
		} else if c == ' ' {
			result += "_"
		}
	}
	if result == "" {
		return "agent"
	}
	return result
}

// ID returns the handoff ID
func (h *Handoff) ID() string {
	return h.id
}

// Kind returns the handoff kind
func (h *Handoff) Kind() HandoffKind {
	return h.kind
}

// TargetAgent returns the target agent
func (h *Handoff) TargetAgent() *Agent {
	return h.targetAgent
}

// ToolName returns the tool name for this handoff
func (h *Handoff) ToolName() string {
	return h.toolName
}

// ToolDescription returns the tool description
func (h *Handoff) ToolDescription() string {
	return h.toolDescription
}

// IsEnabled checks if the handoff is enabled
func (h *Handoff) IsEnabled(ctx context.Context) bool {
	if h.isEnabledFunc != nil {
		return h.isEnabledFunc(ctx)
	}
	return h.isEnabled
}

// OnHandoff executes the on_handoff callback
func (h *Handoff) OnHandoff(ctx context.Context, data HandoffInputData) error {
	if h.onHandoff != nil {
		return h.onHandoff(ctx, data)
	}
	return nil
}

// ApplyInputFilter applies the input filter to the data
func (h *Handoff) ApplyInputFilter(ctx context.Context, data HandoffInputData) (HandoffInputData, error) {
	if h.inputFilter != nil {
		return h.inputFilter(ctx, data)
	}
	return data, nil
}

// NestHandoffHistory returns whether to nest conversation history
func (h *Handoff) NestHandoffHistory() bool {
	return h.nestHandoffHistory
}

// ToToolDefinition converts the handoff to a tool definition
func (h *Handoff) ToToolDefinition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FuncDef{
			Name:        h.toolName,
			Description: h.toolDescription,
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Reason for the handoff",
					},
					"context": map[string]interface{}{
						"type":        "string",
						"description": "Additional context for the handoff",
					},
				},
			},
		},
	}
}

// ToolDef represents a tool definition (matching domain.ToolDefinition structure)
type ToolDef struct {
	Type     string  `json:"type"`
	Function FuncDef `json:"function"`
}

// FuncDef represents a function definition
type FuncDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToDomainTool converts ToolDef to domain.ToolDefinition
func (t ToolDef) ToDomainTool() domain.ToolDefinition {
	return domain.ToolDefinition{
		Type: t.Type,
		Function: domain.ToolFunction{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		},
	}
}
