package agent

import "testing"

func TestToolChoiceForIntent(t *testing.T) {
	if got := toolChoiceForIntent(nil, 0); got != "" {
		t.Fatalf("expected empty tool choice for nil intent, got %q", got)
	}
	if got := toolChoiceForIntent(&IntentRecognitionResult{Transition: "tool_first"}, 0); got != "required" {
		t.Fatalf("expected required for tool_first, got %q", got)
	}
	if got := toolChoiceForIntent(&IntentRecognitionResult{Transition: "prefer_tooling"}, 0); got != "required" {
		t.Fatalf("expected required for prefer_tooling, got %q", got)
	}
	if got := toolChoiceForIntent(&IntentRecognitionResult{Transition: "tool_first"}, 1); got != "" {
		t.Fatalf("expected empty tool choice after round 0, got %q", got)
	}
}

func TestPreferredEntryAgentForIntent(t *testing.T) {
	intent := &IntentRecognitionResult{PreferredAgent: defaultOperatorAgentName}
	if got := preferredEntryAgentForIntent(intent); got != defaultOperatorAgentName {
		t.Fatalf("expected operator, got %q", got)
	}
	if got := preferredEntryAgentForIntent(&IntentRecognitionResult{PreferredAgent: "Unknown"}); got != "" {
		t.Fatalf("expected empty preferred agent for unknown value, got %q", got)
	}
}
