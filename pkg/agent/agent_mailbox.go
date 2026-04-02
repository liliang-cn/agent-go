package agent

import (
	"context"
	"fmt"
	"strings"
)

type agentMailbox struct {
	inbox    chan agentMailboxDelivery
	messages []*AgentMessage
}

type agentMailboxDelivery struct {
	message *AgentMessage
	acked   chan struct{}
}

func (m *TeamManager) ensureAgentMailbox(agentName string) *agentMailbox {
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

func (m *TeamManager) runAgentMailbox(agentName string, mailbox *agentMailbox) {
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

func (m *TeamManager) SendAgentMessage(fromAgent, toAgent, content string, metadata map[string]interface{}) (*AgentMessage, error) {
	message := newAgentProtocolMessage(fromAgent, toAgent, AgentMessageTypeEvent, content, nil, metadata)
	return m.SendStructuredAgentMessage(message)
}

func (m *TeamManager) SendStructuredAgentMessage(message *AgentMessage) (*AgentMessage, error) {
	message = normalizeAgentMessage(message)
	if err := validateAgentMessage(message); err != nil {
		return nil, err
	}
	if _, err := m.GetAgentByName(message.ToAgent); err != nil {
		return nil, err
	}

	mailbox := m.ensureAgentMailbox(message.ToAgent)
	if mailbox == nil {
		return nil, fmt.Errorf("mailbox unavailable for %s", message.ToAgent)
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
		if current := m.agentMailboxes[message.ToAgent]; current != nil {
			current.messages = append(current.messages, cloneAgentMessage(message))
		}
		m.mailboxMu.Unlock()
	}

	return cloneAgentMessage(message), nil
}

func (m *TeamManager) GetAgentMessages(agentName string, limit int, consume bool) ([]*AgentMessage, error) {
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

func (m *TeamManager) registerAgentMessagingTools(svc *Service, model *AgentModel) {
	if svc == nil || model == nil {
		return
	}

	register := func(name, description string, parameters map[string]interface{}, metadata ToolMetadata, handler func(context.Context, map[string]interface{}) (interface{}, error)) {
		if svc.toolRegistry != nil && svc.toolRegistry.Has(name) {
			return
		}
		svc.AddToolWithMetadata(name, description, parameters, handler, metadata)
	}

	register("send_agent_message", "Send a structured built-in message to another agent's mailbox for asynchronous coordination.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_name": map[string]interface{}{
				"type":        "string",
				"description": "The target agent name.",
			},
			"message_type": map[string]interface{}{
				"type":        "string",
				"description": "Structured message type: " + agentMessageProtocolSummary() + ".",
			},
			"correlation_id": map[string]interface{}{
				"type":        "string",
				"description": "Correlation id shared across related request/response/progress messages.",
			},
			"reply_to": map[string]interface{}{
				"type":        "string",
				"description": "Original message id this message is replying to.",
			},
			"priority": map[string]interface{}{
				"type":        "string",
				"description": "Priority: low, normal, high, urgent.",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Human-readable message summary.",
			},
			"payload": map[string]interface{}{
				"type":        "object",
				"description": "Structured payload for the receiving agent.",
			},
			"metadata": map[string]interface{}{
				"type":        "object",
				"description": "Optional transport metadata for tracing or workflow hints.",
			},
		},
		"required": []string{"agent_name"},
	}, ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		targetAgent := getStringArg(args, "agent_name")
		if targetAgent == "" {
			return nil, fmt.Errorf("agent_name is required")
		}

		sourceAgent := strings.TrimSpace(model.Name)
		if current := getCurrentAgent(ctx); current != nil && strings.TrimSpace(current.Name()) != "" {
			sourceAgent = strings.TrimSpace(current.Name())
		}

		payload, _ := args["payload"].(map[string]interface{})
		metadata, _ := args["metadata"].(map[string]interface{})
		sent, err := m.SendStructuredAgentMessage(&AgentMessage{
			FromAgent:     sourceAgent,
			ToAgent:       targetAgent,
			MessageType:   AgentMessageType(getStringArg(args, "message_type")),
			CorrelationID: getStringArg(args, "correlation_id"),
			ReplyTo:       getStringArg(args, "reply_to"),
			Priority:      AgentMessagePriority(getStringArg(args, "priority")),
			Content:       getStringArg(args, "message"),
			Payload:       cloneAgentMessagePayload(payload),
			Metadata:      cloneAgentMessageMetadata(metadata),
		})
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"id":               sent.ID,
			"protocol_version": sent.ProtocolVersion,
			"from_agent":       sent.FromAgent,
			"to_agent":         sent.ToAgent,
			"message_type":     sent.MessageType,
			"correlation_id":   sent.CorrelationID,
			"reply_to":         sent.ReplyTo,
			"priority":         sent.Priority,
			"content":          sent.Content,
			"payload":          cloneAgentMessagePayload(sent.Payload),
			"metadata":         cloneAgentMessageMetadata(sent.Metadata),
			"created_at":       sent.CreatedAt,
		}, nil
	})

	register("get_agent_messages", "Read pending structured mailbox messages that other agents sent to you.", map[string]interface{}{
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
	}, ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
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
				"id":               msg.ID,
				"protocol_version": msg.ProtocolVersion,
				"from_agent":       msg.FromAgent,
				"to_agent":         msg.ToAgent,
				"message_type":     msg.MessageType,
				"correlation_id":   msg.CorrelationID,
				"reply_to":         msg.ReplyTo,
				"priority":         msg.Priority,
				"content":          msg.Content,
				"payload":          cloneAgentMessagePayload(msg.Payload),
				"metadata":         cloneAgentMessageMetadata(msg.Metadata),
				"created_at":       msg.CreatedAt,
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
	cloned.Payload = cloneAgentMessagePayload(msg.Payload)
	cloned.Metadata = cloneAgentMessageMetadata(msg.Metadata)
	return &cloned
}

func cloneAgentMessagePayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
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
