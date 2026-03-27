package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AgentMessage struct {
	ID        string                 `json:"id"`
	FromAgent string                 `json:"from_agent"`
	ToAgent   string                 `json:"to_agent"`
	Content   string                 `json:"content"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

type agentMailbox struct {
	inbox    chan agentMailboxDelivery
	messages []*AgentMessage
}

type agentMailboxDelivery struct {
	message *AgentMessage
	acked   chan struct{}
}

func (m *SquadManager) ensureAgentMailbox(agentName string) *agentMailbox {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return nil
	}

	m.mailboxMu.Lock()
	defer m.mailboxMu.Unlock()

	if mailbox := m.agentMailboxes[agentName]; mailbox != nil {
		return mailbox
	}

	mailbox := &agentMailbox{
		inbox: make(chan agentMailboxDelivery, 64),
	}
	m.agentMailboxes[agentName] = mailbox
	go m.runAgentMailbox(agentName, mailbox)
	return mailbox
}

func (m *SquadManager) runAgentMailbox(agentName string, mailbox *agentMailbox) {
	for delivery := range mailbox.inbox {
		m.mailboxMu.Lock()
		current := m.agentMailboxes[agentName]
		if current == mailbox {
			current.messages = append(current.messages, cloneAgentMessage(delivery.message))
		}
		m.mailboxMu.Unlock()
		if delivery.acked != nil {
			close(delivery.acked)
		}
	}
}

func (m *SquadManager) SendAgentMessage(fromAgent, toAgent, content string, metadata map[string]interface{}) (*AgentMessage, error) {
	toAgent = strings.TrimSpace(toAgent)
	content = strings.TrimSpace(content)
	if toAgent == "" {
		return nil, fmt.Errorf("target agent is required")
	}
	if content == "" {
		return nil, fmt.Errorf("message content is required")
	}
	if _, err := m.GetAgentByName(toAgent); err != nil {
		return nil, err
	}

	message := &AgentMessage{
		ID:        uuid.NewString(),
		FromAgent: firstNonEmpty(strings.TrimSpace(fromAgent), "System"),
		ToAgent:   toAgent,
		Content:   content,
		Metadata:  cloneAgentMessageMetadata(metadata),
		CreatedAt: time.Now(),
	}

	mailbox := m.ensureAgentMailbox(toAgent)
	if mailbox == nil {
		return nil, fmt.Errorf("mailbox unavailable for %s", toAgent)
	}

	delivery := agentMailboxDelivery{
		message: cloneAgentMessage(message),
		acked:   make(chan struct{}),
	}

	select {
	case mailbox.inbox <- delivery:
		<-delivery.acked
	default:
		m.mailboxMu.Lock()
		if current := m.agentMailboxes[toAgent]; current != nil {
			current.messages = append(current.messages, cloneAgentMessage(message))
		}
		m.mailboxMu.Unlock()
	}

	return cloneAgentMessage(message), nil
}

func (m *SquadManager) GetAgentMessages(agentName string, limit int, consume bool) ([]*AgentMessage, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if _, err := m.GetAgentByName(agentName); err != nil {
		return nil, err
	}

	mailbox := m.ensureAgentMailbox(agentName)
	if mailbox == nil {
		return nil, nil
	}

	m.mailboxMu.Lock()
	defer m.mailboxMu.Unlock()

	current := m.agentMailboxes[agentName]
	if current == nil || len(current.messages) == 0 {
		return nil, nil
	}

	count := len(current.messages)
	if limit > 0 && limit < count {
		count = limit
	}

	selected := make([]*AgentMessage, 0, count)
	for _, msg := range current.messages[:count] {
		selected = append(selected, cloneAgentMessage(msg))
	}

	if consume {
		remaining := append([]*AgentMessage(nil), current.messages[count:]...)
		current.messages = remaining
	}

	return selected, nil
}

func (m *SquadManager) registerAgentMessagingTools(svc *Service, model *AgentModel) {
	if svc == nil || model == nil {
		return
	}

	register := func(name, description string, parameters map[string]interface{}, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if svc.toolRegistry != nil && svc.toolRegistry.Has(name) {
			return
		}
		svc.AddTool(name, description, parameters, handler)
	}

	register("send_agent_message", "Send a short built-in message to another agent's mailbox for asynchronous coordination.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "The target agent name.",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "The short coordination message to send.",
			},
		},
		"required": []string{"agent_name", "message"},
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		targetAgent := getStringArg(args, "agent_name")
		message := getStringArg(args, "message")
		if targetAgent == "" {
			return nil, fmt.Errorf("agent_name is required")
		}
		if message == "" {
			return nil, fmt.Errorf("message is required")
		}

		sourceAgent := strings.TrimSpace(model.Name)
		if current := getCurrentAgent(ctx); current != nil && strings.TrimSpace(current.Name()) != "" {
			sourceAgent = strings.TrimSpace(current.Name())
		}

		sent, err := m.SendAgentMessage(sourceAgent, targetAgent, message, nil)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"message_id": sent.ID,
			"from_agent": sent.FromAgent,
			"to_agent":   sent.ToAgent,
			"content":    sent.Content,
			"created_at": sent.CreatedAt,
		}, nil
	})

	register("get_agent_messages", "Read pending mailbox messages that other agents sent to you.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Optional maximum number of messages to return.",
			},
			"consume": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to remove the returned messages from the inbox. Defaults to true.",
			},
		},
	}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		agentName := strings.TrimSpace(model.Name)
		if current := getCurrentAgent(ctx); current != nil && strings.TrimSpace(current.Name()) != "" {
			agentName = strings.TrimSpace(current.Name())
		}

		consume := true
		if raw, ok := args["consume"].(bool); ok {
			consume = raw
		}

		messages, err := m.GetAgentMessages(agentName, getIntArg(args, "limit", 20), consume)
		if err != nil {
			return nil, err
		}

		out := make([]map[string]interface{}, 0, len(messages))
		for _, msg := range messages {
			out = append(out, map[string]interface{}{
				"id":         msg.ID,
				"from_agent": msg.FromAgent,
				"to_agent":   msg.ToAgent,
				"content":    msg.Content,
				"metadata":   cloneAgentMessageMetadata(msg.Metadata),
				"created_at": msg.CreatedAt,
			})
		}
		return out, nil
	})
}

func cloneAgentMessage(msg *AgentMessage) *AgentMessage {
	if msg == nil {
		return nil
	}
	cloned := *msg
	cloned.Metadata = cloneAgentMessageMetadata(msg.Metadata)
	return &cloned
}

func cloneAgentMessageMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
