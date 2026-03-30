package store

import "testing"

func TestLLMProviderSaveAndLoadModels(t *testing.T) {
	db, err := NewAgentGoDB(t.TempDir() + "/agentgo.db")
	if err != nil {
		t.Fatalf("NewAgentGoDB failed: %v", err)
	}
	defer db.Close()

	provider := &LLMProvider{
		Name:           "qc",
		BaseURL:        "http://example.test/v1",
		Key:            "x",
		ModelName:      "gpt-5.4",
		Models:         []string{"gpt-5.4", "gpt-5-mini", "gpt-5.4"},
		MaxConcurrency: 3,
		Capability:     6,
		Enabled:        true,
	}
	if err := db.SaveProvider(provider); err != nil {
		t.Fatalf("SaveProvider failed: %v", err)
	}

	got, err := db.GetProvider("qc")
	if err != nil {
		t.Fatalf("GetProvider failed: %v", err)
	}

	if got.ModelName != "gpt-5.4" {
		t.Fatalf("expected default model gpt-5.4, got %q", got.ModelName)
	}
	if len(got.Models) != 2 || got.Models[0] != "gpt-5.4" || got.Models[1] != "gpt-5-mini" {
		t.Fatalf("expected models [gpt-5.4 gpt-5-mini], got %#v", got.Models)
	}
}

func TestLLMProviderGetProviderFallsBackToDefaultModelWhenLegacyRowsExist(t *testing.T) {
	db, err := NewAgentGoDB(t.TempDir() + "/agentgo.db")
	if err != nil {
		t.Fatalf("NewAgentGoDB failed: %v", err)
	}
	defer db.Close()

	if _, err := db.db.Exec(`
		INSERT INTO llm_providers (name, base_url, key, model_name, max_concurrency, capability, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "legacy", "http://legacy.test/v1", "x", "legacy-model", 2, 4, true); err != nil {
		t.Fatalf("insert legacy row failed: %v", err)
	}
	if _, err := db.db.Exec(`DELETE FROM llm_provider_models WHERE provider_name = ?`, "legacy"); err != nil {
		t.Fatalf("delete provider models failed: %v", err)
	}

	got, err := db.GetProvider("legacy")
	if err != nil {
		t.Fatalf("GetProvider failed: %v", err)
	}

	if len(got.Models) != 1 || got.Models[0] != "legacy-model" {
		t.Fatalf("expected legacy model fallback, got %#v", got.Models)
	}
}
