package cortexbridge

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// Against a real service with memory: agent-go's memory_save is the one the
// model sees, and CortexDB's memory tools are not offered beside it.
func TestRegisterOnServiceWithMemoryKeepsOneMemoryPath(t *testing.T) {
	t.Setenv("AGENTGO_HOME", t.TempDir())
	svc, err := agent.New("one-memory").WithMemory(agent.WithMemoryStoreType("file")).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if !svc.HasMemory() || !svc.GetToolRegistry().Has("memory_save") {
		t.Fatalf("precondition: service memory=%v memory_save=%v", svc.HasMemory(), svc.GetToolRegistry().Has("memory_save"))
	}

	names, err := Register(svc, newTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		for _, m := range MemoryTools {
			if n == m {
				t.Fatalf("CortexDB's %s registered beside agent-go memory", n)
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("nothing registered")
	}
}
