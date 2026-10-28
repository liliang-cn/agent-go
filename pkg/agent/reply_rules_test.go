package agent

import (
	"context"
	"strings"
	"testing"
)

// The user reads the final answer, and task_blocked's argument is that answer.
// Both the prompt and the tool's own schema have to say so, or the reply comes
// back addressed to the runtime ("searched the tool catalog", "用户只说…").
func TestPromptAndBlockedToolSpeakToTheUser(t *testing.T) {
	t.Parallel()

	svc, err := New("reply-rules").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&constraintLLM{replies: []string{"done"}}).
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer svc.Close()

	prompt := svc.buildSystemPromptForTurn(context.Background(), svc.agent, nil, true)
	for _, want := range []string{ReplyRules, ActOnBehalfRules} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt is missing:\n%s\n--- prompt ---\n%s", want, prompt)
		}
	}

	forbidden := svc.buildSystemPromptForRun(context.Background(), svc.agent,
		&RunConfig{resolvedConstraints: &RunConstraints{ForbidTools: true}})
	if !strings.Contains(forbidden, ReplyRules) {
		t.Error("the reply rules must hold for a run without tools too")
	}

	def, ok := svc.toolRegistry.DefinitionOf("task_blocked")
	if !ok {
		t.Fatal("task_blocked is not registered")
	}
	blocker, _ := def.Function.Parameters["properties"].(map[string]interface{})["blocker"].(map[string]interface{})
	desc, _ := blocker["description"].(string)
	for _, want := range []string{"reply to the user", "written to them"} {
		if !strings.Contains(desc, want) {
			t.Errorf("task_blocked.blocker does not say it is the user's reply (%q missing): %q", want, desc)
		}
	}
	if !strings.Contains(def.Function.Description, "ask them") {
		t.Errorf("task_blocked does not send a missing detail to a question: %q", def.Function.Description)
	}
}
