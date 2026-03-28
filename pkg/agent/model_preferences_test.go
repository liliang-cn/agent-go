package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSelectionHintForAgentModelPrefersExplicitProviderAndModel(t *testing.T) {
	model := &AgentModel{
		PreferredProvider:     "openai_local",
		PreferredModel:        "gpt-oss",
		Model:                 "legacy-mixed",
		RequiredLLMCapability: 4,
	}

	hint := selectionHintForAgentModel(model)
	if hint.PreferredProvider != "openai_local" {
		t.Fatalf("expected preferred provider openai_local, got %q", hint.PreferredProvider)
	}
	if hint.PreferredModel != "gpt-oss" {
		t.Fatalf("expected preferred model gpt-oss, got %q", hint.PreferredModel)
	}
	if hint.MinCapability != 4 {
		t.Fatalf("expected min capability 4, got %d", hint.MinCapability)
	}
}

func TestSelectionHintForAgentModelFallsBackToLegacyModel(t *testing.T) {
	model := &AgentModel{
		Model:                 "legacy-hint",
		RequiredLLMCapability: 2,
	}

	hint := selectionHintForAgentModel(model)
	if hint.PreferredProvider != "legacy-hint" {
		t.Fatalf("expected legacy provider fallback, got %q", hint.PreferredProvider)
	}
	if hint.PreferredModel != "legacy-hint" {
		t.Fatalf("expected legacy model fallback, got %q", hint.PreferredModel)
	}
}

func TestCreateAgentStoresPreferredProviderAndModel(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	manager := NewTeamManager(store)
	if err := manager.SeedDefaultMembers(); err != nil {
		t.Fatalf("seed default members failed: %v", err)
	}

	created, err := manager.CreateAgent(context.Background(), &AgentModel{
		Name:              "Scout",
		Description:       "Standalone scout agent.",
		Instructions:      "Scout clearly.",
		PreferredProvider: "openai_local",
		PreferredModel:    "gpt-oss",
	})
	if err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	loaded, err := manager.GetAgentByName(created.Name)
	if err != nil {
		t.Fatalf("get agent failed: %v", err)
	}
	if loaded.PreferredProvider != "openai_local" {
		t.Fatalf("expected preferred provider openai_local, got %q", loaded.PreferredProvider)
	}
	if loaded.PreferredModel != "gpt-oss" {
		t.Fatalf("expected preferred model gpt-oss, got %q", loaded.PreferredModel)
	}
}
