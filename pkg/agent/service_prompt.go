package agent

import (
	"context"
	"fmt"
	"strings"
)

// buildSystemPrompt constructs the system prompt for the current agent.
// ctx is required when PTC is enabled so available callTool() names can be listed dynamically.
func (s *Service) buildSystemPrompt(ctx context.Context, agent *Agent) string {
	return renderSystemPromptSections(s.buildSystemPromptSections(ctx, agent, systemPromptOptions{
		includePTC: s.ptcIntegration != nil,
	}))
}

func isConciergeAgent(agent *Agent) bool {
	if agent == nil {
		return false
	}
	return strings.EqualFold(agent.Name(), BuiltInConciergeAgentName)
}

func isDispatchOnlyAgent(agent *Agent) bool {
	if agent == nil {
		return false
	}
	return isBuiltInDispatchOnlyAgentName(agent.Name())
}

// buildEnrichedPrompt builds a prompt enriched with memory and RAG results
func (s *Service) buildEnrichedPrompt(goal, memoryContext, ragResult string) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("User Question: %s\n\n", goal))

	if memoryContext != "" {
		prompt.WriteString("--- Relevant Memory ---\n")
		prompt.WriteString(memoryContext)
		prompt.WriteString("\n\n")
	}

	if ragResult != "" {
		prompt.WriteString("--- Knowledge Base Results ---\n")
		prompt.WriteString(ragResult)
		prompt.WriteString("\n\n")
	}

	prompt.WriteString("Please answer the user's question based on the memory and knowledge base information above.")
	prompt.WriteString(" If there's no relevant information, say so honestly.")

	return prompt.String()
}

// buildPTCSystemPrompt builds the system prompt with PTC instructions
func (s *Service) buildPTCSystemPrompt(ctx context.Context) string {
	return renderSystemPromptSections(s.buildSystemPromptSections(ctx, s.agent, systemPromptOptions{includePTC: true}))
}

func (s *Service) buildMemoryPromptNote(ctx context.Context, agent *Agent) string {
	if !s.shouldExposeMemoryTools() {
		return ""
	}

	hasSave := false
	hasRecall := false

	if s.ptcIntegration != nil && s.ptcIntegration.config.Enabled {
		for _, tool := range s.ptcAvailableCallTools(ctx) {
			switch tool.Name {
			case "memory_save":
				hasSave = true
			case "memory_recall":
				hasRecall = true
			}
		}
	} else {
		for _, tool := range s.collectAllAvailableTools(ctx, agent) {
			switch tool.Function.Name {
			case "memory_save":
				hasSave = true
			case "memory_recall":
				hasRecall = true
			}
		}
	}

	if !hasSave && !hasRecall {
		return ""
	}

	lines := []string{"Memory tool usage:"}
	if hasSave {
		lines = append(lines, "- If the user explicitly asks you to remember or save information for future conversations, call `memory_save` with the distilled durable fact or preference.")
		lines = append(lines, "- Also call `memory_save` for concise durable statements that are likely to matter later even without an explicit remember phrase, such as meeting times, deadlines, appointments, or planned events.")
		lines = append(lines, "- Save concise normalized content, not the full transcript. Do not save transient task instructions, one-off meta requests, or duplicate content from the same run.")
	}
	if hasRecall {
		lines = append(lines, "- If the user asks what was previously remembered or asks you to answer from memory, call `memory_recall` before answering.")
	}

	return strings.Join(lines, "\n")
}

func (s *Service) buildAgentMessagingPromptNote(ctx context.Context, agent *Agent) string {
	if s == nil {
		return ""
	}

	hasSend := false
	hasRead := false

	if s.ptcIntegration != nil && s.ptcIntegration.config.Enabled {
		for _, tool := range s.ptcAvailableCallTools(ctx) {
			switch tool.Name {
			case "send_agent_message":
				hasSend = true
			case "get_agent_messages":
				hasRead = true
			}
		}
	} else if s.toolRegistry != nil {
		for _, tool := range s.collectAllAvailableTools(ctx, agent) {
			switch tool.Function.Name {
			case "send_agent_message":
				hasSend = true
			case "get_agent_messages":
				hasRead = true
			}
		}
	}

	if !hasSend && !hasRead {
		return ""
	}

	lines := []string{"Inter-agent messaging:"}
	if hasSend {
		lines = append(lines, "- Use `send_agent_message` to send structured mailbox messages to another named agent without blocking on an inline response.")
		lines = append(lines, "- Supported `message_type` values: "+agentMessageProtocolSummary()+". Use `payload` for structured data, `correlation_id` to tie related work together, and `reply_to` when answering a prior request.")
	}
	if hasRead {
		lines = append(lines, "- Use `get_agent_messages` to read pending structured mailbox items sent to you by other agents before you answer or continue a multi-agent workflow.")
	}
	return strings.Join(lines, "\n")
}
