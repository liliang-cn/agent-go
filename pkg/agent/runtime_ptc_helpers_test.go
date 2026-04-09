package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func TestExtractInlineTaskCompleteResult(t *testing.T) {
	t.Parallel()

	content := "```json \n {}\n```\nThe skill trigger matches memory: the result for `EXACT_SKILL_TRIGGER_123` is `EXACT_SKILL_SECRET_789`.\n\ntask_complete"
	got := extractInlineTaskCompleteResult(content)
	want := "The skill trigger matches memory: the result for `EXACT_SKILL_TRIGGER_123` is `EXACT_SKILL_SECRET_789`."
	if got != want {
		t.Fatalf("extractInlineTaskCompleteResult() = %q, want %q", got, want)
	}
}

func TestExtractPTCTerminalAnswer(t *testing.T) {
	t.Parallel()

	toolResults := []ToolExecutionResult{
		{
			ToolName: "execute_javascript",
			Result:   "Code execution completed.\n**Status:** Success ✅\n**Return Value:** EXACT_SKILL_SECRET_789\n\n**Tool Calls (1):**\n- exact-skill-test ✓\n",
		},
	}

	got := extractPTCTerminalAnswer(toolResults)
	if got != "EXACT_SKILL_SECRET_789" {
		t.Fatalf("extractPTCTerminalAnswer() = %q, want %q", got, "EXACT_SKILL_SECRET_789")
	}
}

func TestShouldShortCircuitPTCToolRoundForInlineTaskComplete(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		svc: &Service{
			ptcIntegration: &PTCIntegration{config: &PTCConfig{Enabled: true}},
		},
	}

	content := "```json \n {}\n```\nThe skill trigger matches memory: the result for `EXACT_SKILL_TRIGGER_123` is `EXACT_SKILL_SECRET_789`.\n\ntask_complete"
	toolCalls := []domain.ToolCall{
		{
			ID:   "tc1",
			Type: "function",
			Function: domain.FunctionCall{
				Name:      "execute_javascript",
				Arguments: map[string]interface{}{"code": "on \n {}"},
			},
		},
	}

	got, ok := r.shouldShortCircuitPTCToolRound(content, toolCalls)
	if !ok {
		t.Fatal("expected shouldShortCircuitPTCToolRound to short-circuit")
	}
	if got != "The skill trigger matches memory: the result for `EXACT_SKILL_TRIGGER_123` is `EXACT_SKILL_SECRET_789`." {
		t.Fatalf("short-circuit result = %q", got)
	}
}
