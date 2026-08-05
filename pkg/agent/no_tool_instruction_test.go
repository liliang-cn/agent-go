package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

func toolNamesOf(defs []domain.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// agentbench `personal-planet`: "Without using any tools, name the largest
// planet in our solar system." The model answered correctly (Jupiter) but got
// there through execute_javascript → execute_javascript → search_available_tools
// → execute_javascript, violating the one explicit instruction in the prompt.
//
// Telling the model again in the system prompt does not fix this — the tools
// are right there in the request. Don't offer them.
func TestExplicitNoToolInstructionWithholdsTools(t *testing.T) {
	t.Parallel()

	svc, err := New("no-tool-instruction").
		WithPTC(true).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLintLLM{replies: []string{""}}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	goals := []string{
		"Without using any tools, name the largest planet in our solar system.",
		"Don't use any tools — what is the capital of France?",
		"不要使用任何工具，直接说出太阳系最大的行星。",
	}

	for _, goal := range goals {
		tools, _ := svc.prepareTurnInputs(context.Background(), svc.agent, nil, goal)
		names := toolNamesOf(tools)
		for _, banned := range []string{"execute_javascript", "search_available_tools"} {
			if containsName(names, banned) {
				t.Errorf("goal %q still offered %s (tools: %v)", goal, banned, names)
			}
		}
		// Terminal tools must survive — the model still has to end the turn.
		if !containsName(names, "task_complete") {
			t.Errorf("goal %q lost task_complete (tools: %v)", goal, names)
		}
	}
}

// The guard keys on an explicit instruction only. An ordinary request — even a
// question — must keep its tools, or this trades one failure mode for a worse
// one.
func TestOrdinaryGoalKeepsTools(t *testing.T) {
	t.Parallel()

	svc, err := New("ordinary-goal").
		WithPTC(true).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&scriptedLintLLM{replies: []string{""}}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	for _, goal := range []string{
		"What is the weather in Tokyo tomorrow?",
		"Use a tool to compute 5 * 4 + 10.",
		"名古屋明天天气怎么样？",
	} {
		tools, _ := svc.prepareTurnInputs(context.Background(), svc.agent, nil, goal)
		if !containsName(toolNamesOf(tools), "execute_javascript") {
			t.Errorf("goal %q lost execute_javascript; the guard is over-triggering", goal)
		}
	}
}
