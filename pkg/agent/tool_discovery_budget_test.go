package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// searchCall builds a single-tool-call generation result for a tool search.
func searchCall(id, query string) *domain.GenerationResult {
	return &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{{
			ID:   id,
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "search_available_tools",
				Arguments: map[string]interface{}{"query": query},
			},
		}},
	}
}

// The discovery *budget* is enforced at the tool handlers (see
// tool_discovery_budget_ptc_test.go). What handleDuplicateToolCalls still owns
// is normalization: a search reworded only by casing, spacing, or word order
// cannot return different tools, so it must collapse rather than re-execute.
func TestToolSearchDedupIgnoresCasingSpacingAndWordOrder(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	seen := map[string]int{}

	filtered, _, _ := svc.handleDuplicateToolCalls(nil, searchCall("call-1", "send email"), seen)
	if len(filtered) != 1 {
		t.Fatalf("expected the first search to execute, got %d", len(filtered))
	}

	filtered, duplicates, _ := svc.handleDuplicateToolCalls(nil, searchCall("call-2", "  Send   EMAIL "), seen)
	if len(filtered) != 0 {
		t.Fatalf("expected a case/space variant to be collapsed, got %d executable calls", len(filtered))
	}
	if len(duplicates) != 1 {
		t.Fatalf("expected 1 synthetic duplicate result, got %d", len(duplicates))
	}

	filtered, _, _ = svc.handleDuplicateToolCalls(nil, searchCall("call-3", "email send"), seen)
	if len(filtered) != 0 {
		t.Fatalf("expected a word-order variant to be collapsed, got %d executable calls", len(filtered))
	}
}
