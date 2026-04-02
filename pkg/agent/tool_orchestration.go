package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type toolCallBatch struct {
	isConcurrencySafe bool
	toolCalls         []domain.ToolCall
}

func (s *Service) partitionToolCalls(toolCalls []domain.ToolCall, session *Session, currentAgent *Agent) []toolCallBatch {
	if len(toolCalls) == 0 {
		return nil
	}

	batches := make([]toolCallBatch, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		safe := s.isConcurrencySafeToolCall(toolCall, session, currentAgent)
		if safe && len(batches) > 0 && batches[len(batches)-1].isConcurrencySafe {
			batches[len(batches)-1].toolCalls = append(batches[len(batches)-1].toolCalls, toolCall)
			continue
		}
		batches = append(batches, toolCallBatch{
			isConcurrencySafe: safe,
			toolCalls:         []domain.ToolCall{toolCall},
		})
	}
	return batches
}

func (s *Service) isConcurrencySafeToolCall(toolCall domain.ToolCall, session *Session, currentAgent *Agent) bool {
	name := strings.TrimSpace(strings.ToLower(toolCall.Function.Name))
	if name == "" {
		return false
	}
	if name == "task_complete" || strings.HasPrefix(name, "transfer_to_") {
		return false
	}

	req := PermissionRequest{
		ToolName:  toolCall.Function.Name,
		ToolArgs:  toolCall.Function.Arguments,
		SessionID: currentSessionID(session),
		AgentID:   currentAgentID(currentAgent, s.agent),
	}

	metadata := s.lookupToolMetadataForAgent(toolCall.Function.Name, currentAgent)
	req.ReadOnly = metadata.ReadOnly
	req.Destructive = metadata.Destructive
	req.ConcurrencySafe = metadata.ConcurrencySafe

	s.permissionMu.RLock()
	policy := s.permissionPolicy
	s.permissionMu.RUnlock()
	if policy == nil {
		policy = DefaultPermissionPolicy
	}
	if policy(req) {
		return false
	}
	if metadata.ConcurrencySafe || metadata.ReadOnly {
		return true
	}

	if metadata.Destructive {
		return false
	}

	return false
}

func (s *Service) lookupToolMetadata(name string) ToolMetadata {
	return s.lookupToolMetadataForAgent(name, nil)
}

func (s *Service) lookupToolMetadataForAgent(name string, currentAgent *Agent) ToolMetadata {
	if currentAgent != nil {
		if metadata := currentAgent.MetadataOf(name); metadata != (ToolMetadata{}) {
			return metadata
		}
	}
	if s.toolRegistry != nil {
		if metadata := s.toolRegistry.MetadataOf(name); metadata != (ToolMetadata{}) {
			return metadata
		}
	}
	if provider, ok := s.mcpService.(MCPToolMetadataProvider); ok {
		if metadata, ok := provider.ToolMetadata(name); ok {
			return metadata
		}
	}
	if metadata, ok := inferDynamicToolMetadata(name); ok {
		return metadata
	}
	if metadata, ok := inferGenericToolMetadata(name); ok {
		return metadata
	}
	return ToolMetadata{}
}

func inferDynamicToolMetadata(name string) (ToolMetadata, bool) {
	lower := strings.TrimSpace(strings.ToLower(name))
	if lower == "" {
		return ToolMetadata{}, false
	}

	if strings.HasPrefix(lower, "mcp_filesystem_") {
		switch {
		case strings.Contains(lower, "read"),
			strings.Contains(lower, "list"),
			strings.Contains(lower, "search"),
			strings.Contains(lower, "tree"),
			strings.Contains(lower, "get_file_info"),
			strings.Contains(lower, "allowed_directories"):
			return ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, true
		default:
			return ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}, true
		}
	}

	if strings.HasPrefix(lower, "mcp_websearch_") {
		return ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, true
	}

	if strings.HasPrefix(lower, "mcp_") {
		switch {
		case strings.Contains(lower, "read"),
			strings.Contains(lower, "list"),
			strings.Contains(lower, "search"),
			strings.Contains(lower, "fetch"),
			strings.Contains(lower, "query"),
			strings.Contains(lower, "get"):
			return ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, true
		case strings.Contains(lower, "write"),
			strings.Contains(lower, "create"),
			strings.Contains(lower, "update"),
			strings.Contains(lower, "delete"),
			strings.Contains(lower, "modify"),
			strings.Contains(lower, "move"),
			strings.Contains(lower, "copy"),
			strings.Contains(lower, "send"),
			strings.Contains(lower, "trigger"):
			return ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}, true
		}
	}

	return ToolMetadata{}, false
}

func inferGenericToolMetadata(name string) (ToolMetadata, bool) {
	lower := strings.TrimSpace(strings.ToLower(name))
	if lower == "" {
		return ToolMetadata{}, false
	}

	switch lower {
	case "rag_query", "memory_recall", "memory_list", "search_available_tools", "task_complete":
		return ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, true
	}

	if strings.Contains(lower, "read") ||
		strings.Contains(lower, "list") ||
		strings.Contains(lower, "get") ||
		strings.Contains(lower, "search") ||
		strings.Contains(lower, "fetch") ||
		strings.Contains(lower, "query") ||
		strings.Contains(lower, "glob") ||
		strings.Contains(lower, "grep") {
		return ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}, true
	}

	if strings.Contains(lower, "write") ||
		strings.Contains(lower, "edit") ||
		strings.Contains(lower, "update") ||
		strings.Contains(lower, "delete") ||
		strings.Contains(lower, "remove") ||
		strings.Contains(lower, "create") ||
		strings.Contains(lower, "modify") ||
		strings.Contains(lower, "save") ||
		strings.Contains(lower, "ingest") ||
		strings.Contains(lower, "bash") ||
		strings.Contains(lower, "shell") ||
		strings.Contains(lower, "terminal") ||
		strings.Contains(lower, "execute") ||
		strings.Contains(lower, "run_") ||
		strings.Contains(lower, "send_") ||
		strings.Contains(lower, "interrupt") ||
		strings.Contains(lower, "stop_") {
		return ToolMetadata{InterruptBehavior: InterruptBehaviorBlock}, true
	}

	return ToolMetadata{}, false
}

func currentSessionID(session *Session) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.GetID())
}

func currentAgentID(currentAgent *Agent, fallback *Agent) string {
	if currentAgent != nil && strings.TrimSpace(currentAgent.ID()) != "" {
		return strings.TrimSpace(currentAgent.ID())
	}
	if fallback != nil {
		return strings.TrimSpace(fallback.ID())
	}
	return ""
}
