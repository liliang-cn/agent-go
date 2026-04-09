package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

type DirectToolExecutionOptions struct {
	OnHandoff func(targetAgent *Agent, reason interface{})
}

func (s *Service) resolveExecutableToolNameForAgent(name string, currentAgent *Agent) string {
	if name == "" || name == "task_complete" || strings.HasPrefix(name, "transfer_to_") {
		return name
	}

	candidates := make([]string, 0, len(currentAgent.Tools())+32)
	for _, def := range currentAgent.Tools() {
		candidates = append(candidates, def.Function.Name)
	}
	for _, info := range s.toolRegistry.ListForCallTool() {
		candidates = append(candidates, info.Name)
	}
	if s.mcpService != nil {
		for _, def := range s.mcpService.ListTools() {
			candidates = append(candidates, def.Function.Name)
		}
	}

	if resolved := resolveClosestToolName(name, candidates); resolved != "" {
		return resolved
	}
	return name
}

func (s *Service) executeDirectToolCall(ctx context.Context, currentAgent *Agent, session *Session, tc domain.ToolCall, opts DirectToolExecutionOptions) (interface{}, error, bool) {
	resolvedToolName := s.resolveExecutableToolNameForAgent(tc.Function.Name, currentAgent)
	ctx = withCurrentSession(ctx, session)

	hookData := HookData{
		ToolName:  resolvedToolName,
		ToolArgs:  tc.Function.Arguments,
		SessionID: currentSessionID(session),
		AgentID:   currentAgentID(currentAgent, s.agent),
	}

	if s.hooks != nil {
		modifiedData, err := s.hooks.EmitWithResult(ctx, HookEventPreToolUse, hookData)
		if err != nil {
			return nil, err, false
		}
		if modifiedData.ToolArgs != nil {
			tc.Function.Arguments = modifiedData.ToolArgs
		}
	}

	if strings.HasPrefix(tc.Function.Name, "transfer_to_") {
		for _, h := range currentAgent.Handoffs() {
			if h.ToolName() != tc.Function.Name {
				continue
			}
			targetAgent := h.TargetAgent()
			reason := tc.Function.Arguments["reason"]
			if session != nil {
				session.AgentID = targetAgent.ID()
			}
			if opts.OnHandoff != nil {
				opts.OnHandoff(targetAgent, reason)
			}
			return nil, nil, true
		}
	}

	metadata := s.lookupToolMetadataForAgent(resolvedToolName, currentAgent)
	if err := s.authorizeTool(ctx, PermissionRequest{
		ToolName:        resolvedToolName,
		ToolArgs:        tc.Function.Arguments,
		SessionID:       currentSessionID(session),
		AgentID:         currentAgentID(currentAgent, s.agent),
		ReadOnly:        metadata.ReadOnly,
		Destructive:     metadata.Destructive,
		ConcurrencySafe: metadata.ConcurrencySafe,
	}); err != nil {
		return nil, err, false
	}

	result, execErr := s.dispatchResolvedTool(ctx, currentAgent, resolvedToolName, tc)

	hookData.ToolResult = result
	hookData.ToolError = execErr
	if s.hooks != nil {
		s.hooks.Emit(HookEventPostToolUse, hookData)
	}

	return result, execErr, false
}

func (s *Service) dispatchResolvedTool(ctx context.Context, currentAgent *Agent, resolvedToolName string, tc domain.ToolCall) (interface{}, error) {
	toolName := tc.Function.Name

	if toolName == "task_complete" {
		res, _ := tc.Function.Arguments["result"].(string)
		if res == "" {
			return "Task complete", nil
		}
		return res, nil
	}
	if handler, ok := currentAgent.GetHandler(resolvedToolName); ok {
		return handler(ctx, tc.Function.Arguments)
	}
	if s.toolRegistry.Has(resolvedToolName) {
		return s.toolRegistry.Call(ctx, resolvedToolName, tc.Function.Arguments)
	}
	if s.isMCPTool(resolvedToolName) {
		return s.mcpService.CallTool(ctx, resolvedToolName, tc.Function.Arguments)
	}
	if s.isSkill(ctx, resolvedToolName) && s.skillsService != nil {
		skillID := strings.TrimPrefix(resolvedToolName, "skill_")
		res, err := s.skillsService.Execute(ctx, &skills.ExecutionRequest{
			SkillID:   skillID,
			Variables: tc.Function.Arguments,
		})
		if err != nil {
			return nil, err
		}
		if session := getCurrentSession(ctx); session != nil {
			s.markRelevantSkillSatisfied(session.GetID(), currentTaskID(session))
		}
		return res.Output, nil
	}
	if toolName == "execute_javascript" && s.ptcIntegration != nil {
		return s.ptcIntegration.ExecuteJavascriptTool(ctx, tc.Function.Arguments)
	}
	if domain.IsToolSearchTool(resolvedToolName) {
		query, _ := tc.Function.Arguments["query"].(string)
		if query == "" {
			return nil, fmt.Errorf("tool search requires a 'query' argument")
		}
		searchType := "regex"
		if resolvedToolName == "tool_search_tool_bm25" {
			searchType = "bm25"
		}
		matchedTools, err := s.toolRegistry.ExecuteToolSearch(query, searchType)
		if err != nil {
			return nil, err
		}
		matchedTools = s.filterToolDefinitionsForAgent(currentAgent, matchedTools)
		var refs []domain.ToolReference
		for _, t := range matchedTools {
			refs = append(refs, domain.ToolReference{ToolName: t.Function.Name})
			if session := getCurrentSession(ctx); session != nil {
				s.toolRegistry.ActivateForSession(session.GetID(), t.Function.Name)
			}
		}
		return domain.ToolSearchResult{ToolReferences: refs}, nil
	}
	return nil, fmt.Errorf("unknown tool: %s", toolName)
}
