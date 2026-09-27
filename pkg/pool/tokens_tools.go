package pool

import (
	"encoding/json"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// EstimateToolsTokens sizes the tool schemas a request carries. They are sent
// with every turn and billed as prompt tokens, and on a tool-heavy agent they
// are thousands of them — an estimate over the messages alone read a third
// below the provider's count on a live run for exactly this reason. Each
// definition is sized as the JSON it is serialised to, plus a small framing
// allowance.
func (tc *TokenCounter) EstimateToolsTokens(tools []domain.ToolDefinition, model string) int {
	if tc == nil || len(tools) == 0 {
		return 0
	}
	total := 0
	for _, t := range tools {
		raw, err := json.Marshal(t)
		if err != nil {
			continue
		}
		total += tc.EstimateTokens(string(raw), model) + 4
	}
	return total
}
