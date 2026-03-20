package agent

import (
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/pool"
)

func selectionHintForAgentModel(model *AgentModel) pool.SelectionHint {
	hint := pool.SelectionHint{}
	if model == nil {
		return hint
	}

	hint.PreferredProvider = strings.TrimSpace(model.PreferredProvider)
	hint.PreferredModel = strings.TrimSpace(model.PreferredModel)
	if hint.PreferredProvider == "" && hint.PreferredModel == "" {
		legacy := strings.TrimSpace(model.Model)
		if legacy != "" {
			hint.PreferredProvider = legacy
			hint.PreferredModel = legacy
		}
	}
	hint.MinCapability = model.RequiredLLMCapability
	return hint
}

func configuredModelLabel(model *AgentModel) string {
	if model == nil {
		return ""
	}
	if preferred := strings.TrimSpace(model.PreferredModel); preferred != "" {
		return preferred
	}
	return strings.TrimSpace(model.Model)
}

func configuredProviderLabel(model *AgentModel) string {
	if model == nil {
		return ""
	}
	return strings.TrimSpace(model.PreferredProvider)
}
