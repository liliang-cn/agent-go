package agent

import (
	"strings"
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

// A repeated non-search, non-terminal tool call may be stateful (e.g. re-reading
// a file after a write in a read-modify-write loop). It must be re-executed, not
// short-circuited into a best-effort answer that aborts the run.
func TestHandleDuplicateNonSearchToolReExecutes(t *testing.T) {
	svc := &Service{}
	seen := map[string]int{
		"read_file:map[path:counter.txt]": 1,
	}
	result := &domain.GenerationResult{
		ToolCalls: []domain.ToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: domain.FunctionCall{
					Name:      "read_file",
					Arguments: map[string]interface{}{"path": "counter.txt"},
				},
			},
		},
	}
	messages := []domain.Message{
		{Role: "tool", Content: `{"content":"1"}`},
	}

	filtered, duplicates, fallback := svc.handleDuplicateToolCalls(messages, result, seen)
	if fallback != "" {
		t.Fatalf("expected no fallback (run must continue), got %q", fallback)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected the duplicate stateful call to re-execute, got %d filtered", len(filtered))
	}
	if len(duplicates) != 0 {
		t.Fatalf("expected no synthetic duplicate results, got %d", len(duplicates))
	}
}

// extractBestEffortAnswer must never surface a raw tool-role result as the answer.
func TestExtractBestEffortAnswerIgnoresToolRole(t *testing.T) {
	messages := []domain.Message{
		{Role: "tool", Content: `{"ok":true,"bytes":1}`},
	}
	got := extractBestEffortAnswer("", messages)
	if strings.Contains(got, "bytes") {
		t.Fatalf("best-effort answer leaked raw tool output: %q", got)
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
