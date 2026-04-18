package agent

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTeamManagerBuildServiceForModelPrefersInjectedLLMAndEmbedder(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	manager := NewTeamManager(store)
	cfg := testAgentConfig(t.TempDir())
	cfg.RAG.Enabled = true
	manager.SetConfig(cfg)

	llm := &serviceExecutionStateTestLLM{}
	manager.SetLLM(llm)
	manager.SetEmbedder(vectorMemoryTestEmbedder{})

	model := &AgentModel{
		ID:           "agent-injected",
		A2AID:        "a2a-injected",
		Name:         "InjectedAgent",
		Kind:         AgentKindAgent,
		Description:  "Injected agent",
		Instructions: "Use injected services.",
		EnableRAG:    true,
		EnableMemory: false,
		EnablePTC:    true,
		EnableMCP:    false,
		EnableA2A:    false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := store.SaveAgentModel(model); err != nil {
		t.Fatalf("SaveAgentModel() error = %v", err)
	}

	svc, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() error = %v", err)
	}

	if svc.llmService != llm {
		t.Fatalf("expected injected llm to be used, got %T", svc.llmService)
	}
	if svc.LLM != llm {
		t.Fatalf("expected public LLM field to use injected llm, got %T", svc.LLM)
	}
	if svc.ragProcessor == nil {
		t.Fatal("expected injected embedder to enable RAG processor construction")
	}
}

func TestTeamManagerSetLLMInvalidatesCachedServices(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	manager := NewTeamManager(store)
	cfg := testAgentConfig(t.TempDir())
	cfg.RAG.Enabled = true
	manager.SetConfig(cfg)
	manager.SetEmbedder(vectorMemoryTestEmbedder{})

	llm1 := &serviceExecutionStateTestLLM{}
	llm2 := &serviceExecutionStateTestLLM{}
	manager.SetLLM(llm1)

	model := &AgentModel{
		ID:           "agent-cache-reset",
		A2AID:        "a2a-cache-reset",
		Name:         "CacheResetAgent",
		Kind:         AgentKindAgent,
		Description:  "Cache reset agent",
		Instructions: "Use injected services.",
		EnableRAG:    true,
		EnableMemory: false,
		EnablePTC:    true,
		EnableMCP:    false,
		EnableA2A:    false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := store.SaveAgentModel(model); err != nil {
		t.Fatalf("SaveAgentModel() error = %v", err)
	}

	svc1, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() first call error = %v", err)
	}

	manager.SetLLM(llm2)
	if len(manager.services) != 0 {
		t.Fatalf("expected service cache to be cleared after SetLLM, got %d entries", len(manager.services))
	}

	svc2, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() second call error = %v", err)
	}

	if svc1 == svc2 {
		t.Fatal("expected a rebuilt service after SetLLM invalidated the cache")
	}
	if svc2.llmService != llm2 {
		t.Fatalf("expected rebuilt service to use new injected llm, got %T", svc2.llmService)
	}
}

func TestTeamManagerSetEmbedderInvalidatesCachedServices(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	manager := NewTeamManager(store)
	cfg := testAgentConfig(t.TempDir())
	cfg.RAG.Enabled = true
	manager.SetConfig(cfg)
	manager.SetLLM(&serviceExecutionStateTestLLM{})
	manager.SetEmbedder(vectorMemoryTestEmbedder{})

	model := &AgentModel{
		ID:           "agent-embedder-reset",
		A2AID:        "a2a-embedder-reset",
		Name:         "EmbedderResetAgent",
		Kind:         AgentKindAgent,
		Description:  "Embedder reset agent",
		Instructions: "Use injected services.",
		EnableRAG:    true,
		EnableMemory: false,
		EnablePTC:    true,
		EnableMCP:    false,
		EnableA2A:    false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := store.SaveAgentModel(model); err != nil {
		t.Fatalf("SaveAgentModel() error = %v", err)
	}

	svc1, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() first call error = %v", err)
	}
	if svc1.ragProcessor == nil {
		t.Fatal("expected injected embedder to create a RAG processor")
	}

	manager.SetEmbedder(nil)
	if len(manager.services) != 0 {
		t.Fatalf("expected service cache to be cleared after SetEmbedder, got %d entries", len(manager.services))
	}

	svc2, err := manager.GetAgentService(model.Name)
	if err != nil {
		t.Fatalf("GetAgentService() second call error = %v", err)
	}
	if svc1 == svc2 {
		t.Fatal("expected a rebuilt service after SetEmbedder invalidated the cache")
	}
	if manager.injectedEmbedder != nil {
		t.Fatal("expected embedder override to be cleared")
	}
}
