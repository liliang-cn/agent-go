package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
)

// buildSystemPrompt constructs the system prompt for the current agent.
// ctx is required when PTC is enabled so available callTool() names can be listed dynamically.
func (s *Service) buildSystemPrompt(ctx context.Context, agent *Agent) string {
	systemCtx := s.buildSystemContext()
	operationalRules := strings.Join([]string{
		"- Call task_complete as soon as you have the final answer. Never keep running after the task is done.",
		"- For file operations use mcp_filesystem_* tools; for web search use mcp_websearch_* tools.",
		"- Skills: calling a skill tool returns step-by-step instructions — follow them, then call task_complete.",
		"- Never repeat the same tool call with identical arguments.",
	}, "\n")
	if isDispatchOnlyAgent(agent) {
		operationalRules = ""
	}

	data := map[string]interface{}{
		"AgentInstructions": agent.Instructions(),
		"OperationalRules":  operationalRules,
		"SystemContext":     systemCtx.FormatForPrompt(),
	}

	rendered, err := s.promptManager.Render(prompt.AgentSystemPrompt, data)
	if err != nil {
		// Fallback
		rendered = agent.Instructions() + "\n\n" + systemCtx.FormatForPrompt()
	}

	// Append PTC instructions when enabled so the LLM knows how to use execute_javascript.
	// Dynamically list what is callable via callTool() so the model doesn't have to guess.
	if s.ptcIntegration != nil {
		availableCallTools := s.ptcAvailableCallTools(ctx)
		if ptcPrompt := s.ptcIntegration.GetPTCSystemPrompt(availableCallTools); ptcPrompt != "" {
			rendered += "\n\n" + ptcPrompt
		}
	}

	if note := s.buildMemoryPromptNote(ctx, agent); note != "" {
		rendered += "\n\n" + note
	}

	if note := s.buildAgentMessagingPromptNote(ctx, agent); note != "" {
		rendered += "\n\n" + note
	}

	if summary := s.buildToolCatalogSummary(ctx); summary != "" {
		rendered += "\n\n" + summary
	}

	if !isDispatchOnlyAgent(agent) {
		if note := s.buildWebSearchPromptNote(agent); note != "" {
			rendered += "\n\n" + note
		}
	}

	return rendered
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
	var sb strings.Builder

	// Base agent instructions
	if s.agent != nil {
		sb.WriteString(s.agent.Instructions())
		sb.WriteString("\n\n")
	}

	// PTC instructions with dynamic tool list
	if s.ptcIntegration != nil && s.ptcIntegration.config.Enabled {
		availableCallTools := s.ptcAvailableCallTools(ctx)
		sb.WriteString(s.ptcIntegration.GetPTCSystemPrompt(availableCallTools))
	}

	if note := s.buildMemoryPromptNote(ctx, s.agent); note != "" {
		sb.WriteString("\n\n")
		sb.WriteString(note)
	}

	if note := s.buildAgentMessagingPromptNote(ctx, s.agent); note != "" {
		sb.WriteString("\n\n")
		sb.WriteString(note)
	}

	if summary := s.buildToolCatalogSummary(ctx); summary != "" {
		sb.WriteString("\n")
		sb.WriteString(summary)
	}

	if note := s.buildWebSearchPromptNote(s.agent); note != "" {
		sb.WriteString("\n\n")
		sb.WriteString(note)
	}

	return sb.String()
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
