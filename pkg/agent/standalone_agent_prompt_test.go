package agent

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

func TestBuildStandaloneAgentPromptOmitsTaskCompleteHintForConcierge(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo"}
	model := &AgentModel{
		Name:         BuiltInConciergeAgentName,
		Instructions: "concierge instructions",
	}

	got := buildStandaloneAgentPrompt(cfg, model)
	if strings.Contains(got, "Call task_complete as soon as you have the final answer.") {
		t.Fatalf("expected concierge standalone prompt to omit task_complete hint, got %q", got)
	}
}

func TestBuildStandaloneAgentPromptKeepsTaskCompleteHintForAssistant(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo"}
	model := &AgentModel{
		Name:         "Assistant",
		Instructions: "assistant instructions",
	}

	got := buildStandaloneAgentPrompt(cfg, model)
	if !strings.Contains(got, "Call task_complete as soon as you have the final answer.") {
		t.Fatalf("expected assistant standalone prompt to keep task_complete hint, got %q", got)
	}
}

func TestBuildStandaloneAgentPromptUsesDedicatedTemplateForStakeholder(t *testing.T) {
	cfg := &config.Config{Home: "/tmp/agentgo"}
	model := &AgentModel{
		Name:         defaultStakeholderAgentName,
		Instructions: "stakeholder instructions",
	}

	got := buildStandaloneAgentPrompt(cfg, model)
	if got != "stakeholder instructions" {
		t.Fatalf("expected stakeholder prompt to use dedicated template, got %q", got)
	}
	if strings.Contains(got, "Runtime context:") {
		t.Fatalf("expected stakeholder prompt to omit runtime context, got %q", got)
	}
	if strings.Contains(got, "Shared writable workspace") {
		t.Fatalf("expected stakeholder prompt to omit workspace hint, got %q", got)
	}
	if strings.Contains(got, "Call task_complete as soon as you have the final answer.") {
		t.Fatalf("expected stakeholder prompt to omit task_complete hint, got %q", got)
	}
}
