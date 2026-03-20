package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestHandleDuplicateToolCallsSearchReturnsSyntheticResult(t *testing.T) {
	svc := &Service{}
	seen := map[string]int{
		"search_available_tools:map[query:web search]": 1,
	}
	result := &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: domain.FunctionCall{
					Name: "search_available_tools",
					Arguments: map[string]interface{}{
						"query": "web search",
					},
				},
			},
		},
	}

	filtered, duplicates, fallback := svc.handleDuplicateToolCalls(nil, result, seen)
	if fallback != "" {
		t.Fatalf("unexpected fallback: %q", fallback)
	}
	if len(filtered) != 0 {
		t.Fatalf("expected no executable tool calls, got %d", len(filtered))
	}
	if len(duplicates) != 1 {
		t.Fatalf("expected 1 duplicate tool result, got %d", len(duplicates))
	}
}

func TestHandleDuplicateToolCallsNonSearchReturnsBestEffortAnswer(t *testing.T) {
	svc := &Service{}
	seen := map[string]int{
		"mcp_web_search:map[query:2024 champions league winner]": 1,
	}
	result := &domain.GenerationResult{
		Content: "The task has been completed.",
		ToolCalls: []domain.ToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: domain.FunctionCall{
					Name: "mcp_web_search",
					Arguments: map[string]interface{}{
						"query": "2024 champions league winner",
					},
				},
			},
		},
	}
	messages := []domain.Message{
		{Role: "tool", Content: "2024年欧冠冠军是皇家马德里。"},
	}

	_, _, fallback := svc.handleDuplicateToolCalls(messages, result, seen)
	want := "2024年欧冠冠军是皇家马德里。"
	if fallback != want {
		t.Fatalf("fallback = %q, want %q", fallback, want)
	}
}

func TestHandleDuplicateToolCallsTaskCompleteReturnsResultWithoutFallbackNoise(t *testing.T) {
	svc := &Service{}
	seen := map[string]int{
		"task_complete:map[result:仓库结构总结完成]": 1,
	}
	result := &domain.GenerationResult{
		Content: "The task has been completed.",
		ToolCalls: []domain.ToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: domain.FunctionCall{
					Name: "task_complete",
					Arguments: map[string]interface{}{
						"result": "仓库结构总结完成",
					},
				},
			},
		},
	}

	filtered, duplicates, fallback := svc.handleDuplicateToolCalls(nil, result, seen)
	if len(filtered) != 0 {
		t.Fatalf("expected no executable tool calls, got %d", len(filtered))
	}
	if len(duplicates) != 0 {
		t.Fatalf("expected no synthetic duplicate results, got %d", len(duplicates))
	}
	if fallback != "仓库结构总结完成" {
		t.Fatalf("fallback = %q, want %q", fallback, "仓库结构总结完成")
	}
}
