package agent

import (
	"fmt"
	"slices"
	"strings"
)

func (m *TeamManager) buildKnownAgentsContext(current *AgentModel) string {
	if m == nil || m.store == nil {
		return ""
	}

	agents, err := m.store.ListAgentModels()
	if err != nil || len(agents) == 0 {
		return ""
	}

	slices.SortFunc(agents, compareAgentModelsForRoster)

	lines := []string{
		"Known agents and abilities in this runtime:",
		"- Coordinate with these named agents when their specialization is a better fit.",
		"- Use built-in messaging tools for lightweight asynchronous coordination when useful.",
	}

	for _, agent := range agents {
		if agent == nil || strings.TrimSpace(agent.Name) == "" {
			continue
		}

		line := fmt.Sprintf("- %s [%s]", agent.Name, strings.ToLower(string(agent.Kind)))
		if desc := strings.TrimSpace(agent.Description); desc != "" {
			line += ": " + desc
		}
		if abilities := summarizeAgentAbilities(agent); abilities != "" {
			line += " Abilities: " + abilities
		}
		if current != nil && strings.EqualFold(strings.TrimSpace(current.Name), strings.TrimSpace(agent.Name)) {
			line += " This is you."
		}
		lines = append(lines, line)
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func summarizeAgentAbilities(model *AgentModel) string {
	if model == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if instructions := strings.TrimSpace(model.Instructions); instructions != "" {
		parts = append(parts, singleLinePromptText(instructions))
	}

	features := make([]string, 0, 5)
	if model.EnableMemory {
		features = append(features, "memory")
	}
	if model.EnableRAG {
		features = append(features, "RAG")
	}
	if model.EnableMCP {
		features = append(features, "MCP tools")
	}
	if model.EnablePTC {
		features = append(features, "PTC")
	}
	if len(model.Skills) > 0 {
		features = append(features, "skills: "+strings.Join(model.Skills, ", "))
	}
	if len(features) > 0 {
		parts = append(parts, "Runtime features: "+strings.Join(features, ", "))
	}

	return strings.TrimSpace(strings.Join(parts, " "))
}
