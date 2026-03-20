package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func buildStandaloneAgentPrompt(cfg *config.Config, model *AgentModel) string {
	if isStakeholderAgentModel(model) {
		return buildStakeholderAgentPrompt(model)
	}

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
		lines = append(lines, "- Call task_complete as soon as you have the final answer.")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func buildStakeholderAgentPrompt(model *AgentModel) string {
	if model == nil {
		return ""
	}
	return strings.TrimSpace(model.Instructions)
}

func shouldIncludeTaskCompleteHint(model *AgentModel) bool {
	if model == nil {
		return false
	}

	switch strings.TrimSpace(strings.ToLower(model.Name)) {
	case strings.ToLower(BuiltInConciergeAgentName):
		return false
	default:
		return true
	}
}

func isStakeholderAgentModel(model *AgentModel) bool {
	if model == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(model.Name), defaultStakeholderAgentName)
}
