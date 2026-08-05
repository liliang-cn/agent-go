package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
)

func buildStandaloneAgentPrompt(cfg *config.Config, model *AgentModel) string {

	lines := []string{
		strings.TrimSpace(model.Instructions),
		"",
		"Runtime context:",
		"- Shared writable workspace: " + cfg.WorkspaceDir(),
		"- AgentGo home: " + cfg.Home,
		"- Stay inside the configured workspace unless the user explicitly asks for another location.",
		"- Use the capabilities that are actually available in the current runtime.",
	}
	if shouldIncludeTaskCompleteHint(model) {
		lines = append(lines,
			FinishOrBlockContract,
			"- Call task_complete as soon as you have the final answer.",
			"- Call task_blocked only when a concrete blocker prevents completion.",
		)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func shouldIncludeTaskCompleteHint(model *AgentModel) bool {
	return model != nil
}
