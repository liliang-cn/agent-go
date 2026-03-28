package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const agentMessageProtocolVersion = "agent-go/v1"

type AgentMessageType string

const (
	AgentMessageTypeRequest  AgentMessageType = "request"
	AgentMessageTypeResponse AgentMessageType = "response"
	AgentMessageTypeEvent    AgentMessageType = "event"
	AgentMessageTypeError    AgentMessageType = "error"
	AgentMessageTypeCancel   AgentMessageType = "cancel"
	AgentMessageTypeProgress AgentMessageType = "progress"
)

type AgentMessagePriority string

const (
	AgentMessagePriorityLow    AgentMessagePriority = "low"
	AgentMessagePriorityNormal AgentMessagePriority = "normal"
	AgentMessagePriorityHigh   AgentMessagePriority = "high"
	AgentMessagePriorityUrgent AgentMessagePriority = "urgent"
)

type AgentMessageProtocolSpec struct {
	MessageType       AgentMessageType     `json:"message_type"`
	Description       string               `json:"description"`
	DefaultPriority   AgentMessagePriority `json:"default_priority"`
	RequiresReplyTo   bool                 `json:"requires_reply_to"`
	TypicalPayloadUse string               `json:"typical_payload_use,omitempty"`
}

var agentMessageProtocolTable = []AgentMessageProtocolSpec{
	{
		MessageType:       AgentMessageTypeRequest,
		Description:       "Asks another agent to perform work or provide information.",
		DefaultPriority:   AgentMessagePriorityNormal,
		TypicalPayloadUse: "instruction, question, requested action, optional constraints",
	},
	{
		MessageType:       AgentMessageTypeResponse,
		Description:       "Returns the outcome for an earlier request.",
		DefaultPriority:   AgentMessagePriorityNormal,
		RequiresReplyTo:   true,
		TypicalPayloadUse: "answer, final result, structured output",
	},
	{
		MessageType:       AgentMessageTypeEvent,
		Description:       "Shares a fact, finding, notification, or other one-way coordination update.",
		DefaultPriority:   AgentMessagePriorityNormal,
		TypicalPayloadUse: "fact, finding, notification, routing hint",
	},
	{
		MessageType:       AgentMessageTypeError,
		Description:       "Reports a failure tied to an earlier request or workflow.",
		DefaultPriority:   AgentMessagePriorityHigh,
		RequiresReplyTo:   true,
		TypicalPayloadUse: "error message, failure code, failing step",
	},
	{
		MessageType:       AgentMessageTypeCancel,
		Description:       "Requests cancellation of an in-flight request or workflow.",
		DefaultPriority:   AgentMessagePriorityHigh,
		RequiresReplyTo:   true,
		TypicalPayloadUse: "reason, cancelled request or correlation id",
	},
	{
		MessageType:       AgentMessageTypeProgress,
		Description:       "Reports in-flight status while work is still running.",
		DefaultPriority:   AgentMessagePriorityLow,
		RequiresReplyTo:   true,
		TypicalPayloadUse: "status, percent complete, next step, waiting condition",
	},
}

var agentMessageProtocolIndex = map[AgentMessageType]AgentMessageProtocolSpec{
	AgentMessageTypeRequest:  agentMessageProtocolTable[0],
	AgentMessageTypeResponse: agentMessageProtocolTable[1],
	AgentMessageTypeEvent:    agentMessageProtocolTable[2],
	AgentMessageTypeError:    agentMessageProtocolTable[3],
	AgentMessageTypeCancel:   agentMessageProtocolTable[4],
	AgentMessageTypeProgress: agentMessageProtocolTable[5],
}

var validAgentMessagePriorities = map[AgentMessagePriority]struct{}{
	AgentMessagePriorityLow:    {},
	AgentMessagePriorityNormal: {},
	AgentMessagePriorityHigh:   {},
	AgentMessagePriorityUrgent: {},
}

type AgentMessage struct {
	ID              string                 `json:"id"`
	ProtocolVersion string                 `json:"protocol_version"`
	FromAgent       string                 `json:"from_agent"`
	ToAgent         string                 `json:"to_agent"`
	MessageType     AgentMessageType       `json:"message_type"`
	CorrelationID   string                 `json:"correlation_id"`
	ReplyTo         string                 `json:"reply_to,omitempty"`
	Priority        AgentMessagePriority   `json:"priority"`
	Content         string                 `json:"content,omitempty"`
	Payload         map[string]interface{} `json:"payload,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
}

func listAgentMessageProtocol() []AgentMessageProtocolSpec {
	out := make([]AgentMessageProtocolSpec, len(agentMessageProtocolTable))
	copy(out, agentMessageProtocolTable)
	return out
}

func agentMessageProtocolSummary() string {
	parts := make([]string, 0, len(agentMessageProtocolTable))
	for _, spec := range agentMessageProtocolTable {
		parts = append(parts, string(spec.MessageType))
	}
	return strings.Join(parts, ", ")
}

func normalizeAgentMessage(msg *AgentMessage) *AgentMessage {
	if msg == nil {
		return nil
	}

	cloned := *msg
	cloned.ProtocolVersion = firstNonEmpty(strings.TrimSpace(cloned.ProtocolVersion), agentMessageProtocolVersion)
	cloned.ID = strings.TrimSpace(cloned.ID)
	cloned.FromAgent = firstNonEmpty(strings.TrimSpace(cloned.FromAgent), "System")
	cloned.ToAgent = strings.TrimSpace(cloned.ToAgent)
	cloned.ReplyTo = strings.TrimSpace(cloned.ReplyTo)
	cloned.Content = strings.TrimSpace(cloned.Content)
	cloned.Payload = cloneAgentMessagePayload(cloned.Payload)
	cloned.Metadata = cloneAgentMessageMetadata(cloned.Metadata)

	if cloned.ID == "" {
		cloned.ID = uuid.NewString()
	}
	if cloned.MessageType == "" {
		cloned.MessageType = AgentMessageTypeEvent
	}
	if cloned.Priority == "" {
		cloned.Priority = defaultAgentMessagePriority(cloned.MessageType)
	}
	if cloned.CorrelationID == "" {
		if cloned.ReplyTo != "" {
			cloned.CorrelationID = cloned.ReplyTo
		} else {
			cloned.CorrelationID = uuid.NewString()
		}
	}
	if len(cloned.Payload) == 0 && cloned.Content != "" {
		cloned.Payload = map[string]interface{}{"text": cloned.Content}
	}
	if cloned.Content == "" {
		for _, key := range []string{"text", "summary", "message"} {
			if raw, ok := cloned.Payload[key]; ok {
				if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
					cloned.Content = strings.TrimSpace(text)
					break
				}
			}
		}
	}
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = time.Now()
	}

	return &cloned
}

func validateAgentMessage(msg *AgentMessage) error {
	msg = normalizeAgentMessage(msg)
	if msg == nil {
		return fmt.Errorf("message is required")
	}
	if strings.TrimSpace(msg.ToAgent) == "" {
		return fmt.Errorf("target agent is required")
	}
	if _, ok := agentMessageProtocolIndex[msg.MessageType]; !ok {
		return fmt.Errorf("unsupported message_type %q", msg.MessageType)
	}
	if _, ok := validAgentMessagePriorities[msg.Priority]; !ok {
		return fmt.Errorf("unsupported priority %q", msg.Priority)
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.Payload) == 0 {
		return fmt.Errorf("message content or payload is required")
	}
	if spec := agentMessageProtocolIndex[msg.MessageType]; spec.RequiresReplyTo && strings.TrimSpace(msg.ReplyTo) == "" {
		return fmt.Errorf("reply_to is required for %s messages", msg.MessageType)
	}
	return nil
}

func defaultAgentMessagePriority(messageType AgentMessageType) AgentMessagePriority {
	if spec, ok := agentMessageProtocolIndex[messageType]; ok && spec.DefaultPriority != "" {
		return spec.DefaultPriority
	}
	return AgentMessagePriorityNormal
}

func newAgentProtocolMessage(fromAgent, toAgent string, messageType AgentMessageType, content string, payload map[string]interface{}, metadata map[string]interface{}) *AgentMessage {
	return normalizeAgentMessage(&AgentMessage{
		FromAgent:   strings.TrimSpace(fromAgent),
		ToAgent:     strings.TrimSpace(toAgent),
		MessageType: messageType,
		Content:     strings.TrimSpace(content),
		Payload:     cloneAgentMessagePayload(payload),
		Metadata:    cloneAgentMessageMetadata(metadata),
	})
}

func newAgentProtocolReply(request *AgentMessage, fromAgent string, messageType AgentMessageType, content string, payload map[string]interface{}, metadata map[string]interface{}) *AgentMessage {
	reply := newAgentProtocolMessage(fromAgent, "", messageType, content, payload, metadata)
	if request == nil {
		return reply
	}
	reply.ToAgent = strings.TrimSpace(request.FromAgent)
	reply.ReplyTo = strings.TrimSpace(request.ID)
	reply.CorrelationID = firstNonEmpty(strings.TrimSpace(request.CorrelationID), strings.TrimSpace(request.ID), reply.CorrelationID)
	reply.Priority = defaultAgentMessagePriority(messageType)
	return normalizeAgentMessage(reply)
}
