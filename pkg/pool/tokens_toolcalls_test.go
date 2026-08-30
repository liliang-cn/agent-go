package pool

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// A tool-using agent's largest messages carry nothing in Content: the payload
// is the tool call's arguments. Estimating those at zero made the compaction
// threshold unreachable, so a run's history grew without bound.
func TestEstimateCountsToolCallArguments(t *testing.T) {
	tc := NewTokenCounter()
	file := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) { /* body */ }\n", 60)

	var msgs []domain.Message
	for range 10 {
		msgs = append(msgs, domain.Message{
			Role: "assistant",
			ToolCalls: []domain.ToolCall{{
				ID: "c1", Type: "function",
				Function: domain.FunctionCall{
					Name:      "fs_write",
					Arguments: map[string]interface{}{"path": "internal/api/h.go", "content": file},
				},
			}},
		})
		msgs = append(msgs, domain.Message{Role: "tool", ToolCallID: "c1", Content: `{"ok":true}`})
	}

	est := tc.EstimateConversationTokens(msgs, "")

	// Ten copies of a ~4KB file is on the order of 10k tokens. The old
	// estimator returned 150 for this exact input.
	if est < 5000 {
		t.Fatalf("estimate %d is far below the ~10k tokens actually being sent; "+
			"tool call arguments are still invisible", est)
	}
	// And it must not run away either — this is a budget, not a bill.
	if est > 30000 {
		t.Fatalf("estimate %d is implausibly high for ~10k tokens of payload", est)
	}
}

// Nested arguments count too: a batch edit passes a list of objects.
func TestEstimateWalksNestedToolArguments(t *testing.T) {
	tc := NewTokenCounter()
	// Real source, not a degenerate string: a tokenizer merges "xxxx..."
	// hard enough that it stops resembling the payloads this guards.
	body := strings.Repeat("if err != nil { return nil, fmt.Errorf(\"load: %w\", err) }\n", 100)
	msgs := []domain.Message{{
		Role: "assistant",
		ToolCalls: []domain.ToolCall{{
			Function: domain.FunctionCall{
				Name: "fs_batch_edit",
				Arguments: map[string]interface{}{
					"edits": []interface{}{
						map[string]interface{}{"path": "a.go", "content": body},
						map[string]interface{}{"path": "b.go", "content": body},
					},
				},
			},
		}},
	}}
	if est := tc.EstimateConversationTokens(msgs, ""); est < 3000 {
		t.Fatalf("estimate %d ignores arguments nested inside a list", est)
	}
}

// Reasoning content is billed as output and sits in the history; it must not
// be invisible either.
func TestEstimateCountsReasoningContent(t *testing.T) {
	tc := NewTokenCounter()
	msgs := []domain.Message{{Role: "assistant", ReasoningContent: strings.Repeat("think ", 2000)}}
	if est := tc.EstimateConversationTokens(msgs, ""); est < 1000 {
		t.Fatalf("estimate %d ignores reasoning content", est)
	}
}
