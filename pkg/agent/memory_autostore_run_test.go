package agent_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

// countingMemory answers retrieval with nothing and counts the automatic
// writes the loop asks for. Anything else the loop might call panics on the
// embedded nil, which is the point: this test is about the write side.
type countingMemory struct {
	domain.MemoryService
	stores atomic.Int32
}

func (m *countingMemory) RetrieveAndInject(context.Context, string, string) (string, []*domain.MemoryWithScore, error) {
	return "", nil, nil
}
func (m *countingMemory) RetrieveAndInjectWithContext(context.Context, string, domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, error) {
	return "", nil, nil
}
func (m *countingMemory) StoreIfWorthwhile(context.Context, *domain.MemoryStoreRequest) error {
	m.stores.Add(1)
	return nil
}
func (m *countingMemory) RetrieveAndInjectWithContextAndLogic(context.Context, string, domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, string, error) {
	return "", nil, "", nil
}
func (m *countingMemory) Close() error { return nil }

// The automatic write runs on an ordinary run and not on one started
// WithoutMemoryAutoStore; the Builder-level switch is not touched.
func TestWithoutMemoryAutoStoreSkipsTheWriteForOneRun(t *testing.T) {
	mem := &countingMemory{}
	llm := extensiontest.Script(extensiontest.Answer("noted"), extensiontest.Answer("noted"))
	svc, err := extensiontest.NewServiceWithBuilder(agent.New("worker").WithLLM(llm).WithMemoryService(mem), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	extensiontest.Run(t, svc, "an order from the queen", agent.WithoutMemoryAutoStore())
	extensiontest.Run(t, svc, "a person at the keyboard")
	_ = svc.Close() // drains the background writer, so the count is final
	if got := mem.stores.Load(); got != 1 {
		t.Fatalf("automatic writes = %d, want 1 (the ordinary run only)", got)
	}
}
