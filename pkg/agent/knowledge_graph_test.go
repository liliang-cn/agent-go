package agent

import (
	"context"
	"testing"
)

// Non-graph store path: graph_recall tool registers and KnowledgeRecall
// gracefully degrades to plain memory search (no cortexdb/graph needed).
func TestGraphRecallToolAndFallback(t *testing.T) {
	svc, err := New("kg-fallback").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&serviceExecutionStateTestLLM{}).
		WithMemory(WithMemoryStoreType("file")).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	defer svc.Close()

	// graph_recall tool is registered (idempotent) so the loop can query it.
	RegisterGraphRecallTool(svc)
	RegisterGraphRecallTool(svc)
	if !svc.toolRegistry.Has("graph_recall") {
		t.Fatal("expected graph_recall tool to be registered")
	}

	// KnowledgeRecall routes to the memory service (file store doesn't implement
	// the graph interface, so it falls back to Search). We only assert routing
	// reached the memory service rather than the "memory not enabled" guard.
	_, err = svc.KnowledgeRecall(context.Background(), "anything", 5)
	if err != nil && err.Error() == "memory is not enabled for this agent" {
		t.Fatalf("KnowledgeRecall should route to memory service, got: %v", err)
	}
}

// KnowledgeRecall must error clearly when memory isn't enabled.
func TestKnowledgeRecallWithoutMemory(t *testing.T) {
	svc, err := New("kg-nomem").
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&serviceExecutionStateTestLLM{}).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	defer svc.Close()

	if _, err := svc.KnowledgeRecall(context.Background(), "q", 5); err == nil {
		t.Fatal("expected error when memory is not enabled")
	}
}
