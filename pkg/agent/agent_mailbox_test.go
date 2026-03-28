package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentMessagingToolsDeliverMailboxMessages(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	concierge, err := manager.GetAgentService(BuiltInConciergeAgentName)
	if err != nil {
		t.Fatalf("get concierge service failed: %v", err)
	}
	archivist, err := manager.GetAgentService(defaultArchivistAgentName)
	if err != nil {
		t.Fatalf("get archivist service failed: %v", err)
	}

	if _, err := concierge.toolRegistry.Call(context.Background(), "send_agent_message", map[string]interface{}{
		"agent_name":   defaultArchivistAgentName,
		"message_type": string(AgentMessageTypeRequest),
		"priority":     string(AgentMessagePriorityHigh),
		"message":      "Remember to normalize the saved schedule fact.",
		"payload": map[string]interface{}{
			"instruction": "normalize_schedule_fact",
		},
	}); err != nil {
		t.Fatalf("send_agent_message failed: %v", err)
	}

	raw, err := archivist.toolRegistry.Call(context.Background(), "get_agent_messages", map[string]interface{}{
		"limit":   10,
		"consume": true,
	})
	if err != nil {
		t.Fatalf("get_agent_messages failed: %v", err)
	}

	messages, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("unexpected message payload: %#v", raw)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one mailbox message, got %#v", messages)
	}
	if got := messages[0]["from_agent"]; got != BuiltInConciergeAgentName {
		t.Fatalf("unexpected source agent: %#v", messages[0])
	}
	if got := messages[0]["to_agent"]; got != defaultArchivistAgentName {
		t.Fatalf("unexpected target agent: %#v", messages[0])
	}
	if got := messages[0]["content"]; got != "Remember to normalize the saved schedule fact." {
		t.Fatalf("unexpected message content: %#v", messages[0])
	}
	if got := messages[0]["message_type"]; got != AgentMessageTypeRequest {
		t.Fatalf("unexpected message type: %#v", messages[0])
	}
	if got := messages[0]["priority"]; got != AgentMessagePriorityHigh {
		t.Fatalf("unexpected priority: %#v", messages[0])
	}

	raw, err = archivist.toolRegistry.Call(context.Background(), "get_agent_messages", map[string]interface{}{
		"limit":   10,
		"consume": true,
	})
	if err != nil {
		t.Fatalf("second get_agent_messages failed: %v", err)
	}
	messages, ok = raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("unexpected second message payload: %#v", raw)
	}
	if len(messages) != 0 {
		t.Fatalf("expected consumed mailbox to be empty, got %#v", messages)
	}
}

func TestBuiltServicePromptIncludesKnownAgentsCatalog(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	manager := NewTeamManager(store)
	manager.SetConfig(testAgentConfig(t.TempDir()))
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	svc, err := manager.GetAgentService(BuiltInConciergeAgentName)
	if err != nil {
		t.Fatalf("get concierge service failed: %v", err)
	}

	prompt := svc.agent.Instructions()
	if !strings.Contains(prompt, "Known agents and abilities in this runtime:") {
		t.Fatalf("expected system prompt to include agent catalog, got %q", prompt)
	}
	if !strings.Contains(prompt, "PromptOptimizer [agent]") {
		t.Fatalf("expected system prompt to include PromptOptimizer entry, got %q", prompt)
	}
	if !strings.Contains(prompt, "Archivist [agent]") {
		t.Fatalf("expected system prompt to include Archivist entry, got %q", prompt)
	}
	if !strings.Contains(prompt, "Abilities:") {
		t.Fatalf("expected system prompt to include agent abilities, got %q", prompt)
	}
}
