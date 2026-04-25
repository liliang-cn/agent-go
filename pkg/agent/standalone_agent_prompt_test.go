package agent

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func TestBuildStandaloneAgentPromptOmitsTaskCompleteHintForDispatcher(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo"}
	model := &AgentModel{
		Name:         BuiltInDispatcherAgentName,
		Instructions: "dispatcher instructions",
	}

	got := buildStandaloneAgentPrompt(cfg, model)
	if strings.Contains(got, "Call task_complete as soon as you have the final answer.") {
		t.Fatalf("expected dispatcher standalone prompt to omit task_complete hint, got %q", got)
	}
}

func TestBuildStandaloneAgentPromptKeepsTaskCompleteHintForResponder(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo"}
	model := &AgentModel{
		Name:         "Responder",
		Instructions: "assistant instructions",
	}

	got := buildStandaloneAgentPrompt(cfg, model)
	if !strings.Contains(got, "Call task_complete as soon as you have the final answer.") {
		t.Fatalf("expected assistant standalone prompt to keep task_complete hint, got %q", got)
	}
	if !strings.Contains(got, "Finish-Or-Block Contract:") || !strings.Contains(got, "task_blocked") {
		t.Fatalf("expected assistant standalone prompt to include finish-or-block contract, got %q", got)
	}
}

func TestBuildStandaloneAgentPromptUsesDedicatedTemplateForEvaluator(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo"}
	model := &AgentModel{
		Name:         defaultEvaluatorAgentName,
		Instructions: "evaluator instructions",
	}

	got := buildStandaloneAgentPrompt(cfg, model)
	if got != "evaluator instructions" {
		t.Fatalf("expected evaluator prompt to use dedicated template, got %q", got)
	}
	if strings.Contains(got, "Runtime context:") {
		t.Fatalf("expected evaluator prompt to omit runtime context, got %q", got)
	}
	if strings.Contains(got, "Shared writable workspace") {
		t.Fatalf("expected evaluator prompt to omit workspace hint, got %q", got)
	}
	if strings.Contains(got, "Call task_complete as soon as you have the final answer.") {
		t.Fatalf("expected evaluator prompt to omit task_complete hint, got %q", got)
	}
}
