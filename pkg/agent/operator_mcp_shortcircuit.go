package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func (s *Service) tryDirectOperatorMCPExecution(ctx context.Context, goal string) (string, bool, error) {
	if s == nil || s.agent == nil || !strings.EqualFold(strings.TrimSpace(s.agent.Name()), defaultOperatorAgentName) {
		return "", false, nil
	}
	if s.llmService == nil || s.mcpService == nil {
		return "", false, nil
	}

	goal = normalizeTaskPrompt(goal)
	if goal == "" {
		return "", false, nil
	}
	coreGoal := stripExecutionContract(goal)
	if looksLikeInformationSeekingQuery(coreGoal) {
		return "", false, nil
	}

	available := make([]domain.ToolDefinition, 0)
	for _, tool := range s.mcpService.ListTools() {
		if s.isToolAllowedForAgent(s.agent, tool.Function.Name) {
			available = append(available, tool)
		}
	}
	if len(available) == 0 {
		return "", false, nil
	}

	if chosenTool := s.chooseDirectMCPToolName(ctx, coreGoal, available); chosenTool != "" {
		return summarizeDirectMCPToolCalls(ctx, s.mcpService, []domain.ToolCall{
			{
				ID:   "direct-mcp-call",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      chosenTool,
					Arguments: map[string]interface{}{},
				},
			},
		})
	}

	messages := []domain.Message{
		{
			Role: "system",
			Content: strings.TrimSpace(`You are mapping an operational request onto the exact callable MCP tools currently exposed by the runtime.
Rules:
- Treat the visible tool definitions as the authoritative source of what can be executed.
- Do not invent hidden API names or generic method names when concrete callable tool names are already exposed.
- If one or more concrete tools could satisfy the request, call them directly.
- For action requests, prefer the most semantically aligned action tool over visibility, listing, or inspection tools unless those are necessary prerequisites.
- Use inspection tools only when needed to disambiguate or verify.
- If no available tool can plausibly satisfy the request, do not call any tool and reply exactly: NO_TOOL_MATCH.

Visible callable tools:
` + formatVisibleToolList(available)),
		},
		{
			Role:    "user",
			Content: coreGoal,
		},
	}

	result, err := s.llmService.GenerateWithTools(ctx, messages, available, s.toolGenerationOptions(0.1, 800, "required"))
	if err != nil {
		return "", false, err
	}
	if result == nil {
		return "", false, nil
	}
	if len(result.ToolCalls) == 0 {
		if strings.Contains(strings.ToUpper(strings.TrimSpace(result.Content)), "NO_TOOL_MATCH") {
			return "", false, nil
		}
		return "", false, nil
	}

	return summarizeDirectMCPToolCalls(ctx, s.mcpService, result.ToolCalls)
}

func (s *Service) chooseDirectMCPToolName(ctx context.Context, goal string, tools []domain.ToolDefinition) string {
	if s == nil || s.llmService == nil || len(tools) == 0 {
		return ""
	}

	toolList := formatVisibleToolList(tools)
	prompt := strings.TrimSpace(`Choose the single best callable tool name for the user's request.
Rules:
- Return only one exact tool name from the provided list, or return exactly NO_TOOL_MATCH.
- Use the provided tool names as the only allowed options.
- Do not invent new method names.
- Prefer the most semantically aligned action tool for direct action requests.

Available tools:
` + toolList + `

User request:
` + goal)

	raw, err := s.llmService.Generate(ctx, prompt, &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   80,
	})
	if err != nil {
		return ""
	}

	selected := strings.TrimSpace(raw)
	if strings.EqualFold(selected, "NO_TOOL_MATCH") {
		return ""
	}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			continue
		}
		if strings.EqualFold(selected, name) || strings.Contains(selected, name) {
			return name
		}
	}
	return ""
}

func summarizeDirectMCPToolCalls(ctx context.Context, mcpSvc MCPToolExecutor, toolCalls []domain.ToolCall) (string, bool, error) {
	if mcpSvc == nil || len(toolCalls) == 0 {
		return "", false, nil
	}

	lines := make([]string, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		result, err := mcpSvc.CallTool(ctx, toolCall.Function.Name, toolCall.Function.Arguments)
		if err != nil {
			return "", true, err
		}
		lines = append(lines, fmt.Sprintf("%s: %s", toolCall.Function.Name, strings.TrimSpace(formatResultForContent(result))))
	}
	return strings.Join(lines, "\n"), true, nil
}

// stripExecutionContract removes the "Execution contract:" block appended by
// buildFinalBuiltInDispatchPrompt so that downstream checks see only the
// original user instruction, not the meta-instructions.
func stripExecutionContract(goal string) string {
	const marker = "\n\nExecution contract:"
	if idx := strings.Index(goal, marker); idx >= 0 {
		return strings.TrimSpace(goal[:idx])
	}
	return goal
}

func formatVisibleToolList(tools []domain.ToolDefinition) string {
	lines := make([]string, 0, len(tools))
	for _, tool := range tools {
		lines = append(lines, fmt.Sprintf("- %s: %s", tool.Function.Name, strings.TrimSpace(tool.Function.Description)))
	}
	return strings.Join(lines, "\n")
}
