package agent

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func buildDebugPrompt(info AgentInfo, tools []domain.ToolDefinition, messages []domain.Message) string {
	var promptBuilder strings.Builder
	fmt.Fprintf(&promptBuilder, "MODEL: %s (%s)\n", info.Model, info.BaseURL)
	fmt.Fprintf(&promptBuilder, "=== TOOLS (%d) ===\n", len(tools))
	for _, t := range tools {
		fmt.Fprintf(&promptBuilder, "  • %s: %s\n", t.Function.Name, t.Function.Description)
	}
	fmt.Fprintf(&promptBuilder, "\n=== MESSAGES ===\n")
	for _, m := range messages {
		fmt.Fprintf(&promptBuilder, "[%s]:\n%s\n", strings.ToUpper(m.Role), m.Content)
	}
	return promptBuilder.String()
}

func buildDebugResponse(content string, toolCalls []domain.ToolCall, history []domain.Message) string {
	var respBuilder strings.Builder
	fmt.Fprintf(&respBuilder, "CONTENT: %s\n", content)
	if len(toolCalls) > 0 {
		fmt.Fprintf(&respBuilder, "TOOL CALLS:\n")
		for _, tc := range toolCalls {
			fmt.Fprintf(&respBuilder, "  - %s(%v)\n", tc.Function.Name, tc.Function.Arguments)
		}
	}
	if len(history) > 0 {
		fmt.Fprintf(&respBuilder, "\n=== MESSAGES IN HISTORY (%d) ===\n", len(history))
		for i, m := range history {
			fmt.Fprintf(&respBuilder, "%d. [%s] %s\n", i+1, strings.ToUpper(m.Role), m.Content)
		}
	}
	return respBuilder.String()
}
