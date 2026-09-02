package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// buildMemoryServiceOver wires a real memory.Service over a file store in
// dir, which is what a host swapping backends actually hands over.
func buildMemoryServiceOver(t *testing.T, dir string) *memory.Service {
	t.Helper()
	st, err := store.NewFileMemoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.MinScore = 0
	return memory.NewService(st, nil, nil, cfg)
}

// The test that matters, and the one a store-level round trip passes while
// the agent sees nothing: assert on the INJECTED TEXT, before and after.
func TestSwappingTheBackendChangesWhatTheAgentIsShown(t *testing.T) {
	ctx := context.Background()
	svc, err := New("swap").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := buildMemoryServiceOver(t, t.TempDir())
	second := buildMemoryServiceOver(t, t.TempDir())

	if err := first.Add(ctx, &domain.Memory{
		ID: "a", Type: domain.MemoryTypeFact, Content: "the gateway port is 47821",
		SessionID: "s", CreatedAt: time.Now(), Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	if err := second.Add(ctx, &domain.Memory{
		ID: "b", Type: domain.MemoryTypeFact, Content: "the gateway port is 51000",
		SessionID: "s", CreatedAt: time.Now(), Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}

	inject := func() string {
		text, _, err := svc.memory().RetrieveAndInject(ctx, "what is the gateway port?", "s")
		if err != nil {
			t.Fatalf("RetrieveAndInject: %v", err)
		}
		return text
	}

	svc.SetMemoryService(first)
	before := inject()
	if !strings.Contains(before, "47821") {
		t.Fatalf("the first backend's memory was not injected:\n%s", before)
	}

	previous := svc.SetMemoryService(second)
	if previous != domain.MemoryService(first) {
		t.Error("SetMemoryService did not hand back the service it replaced")
	}
	after := inject()
	if !strings.Contains(after, "51000") {
		t.Fatalf("the second backend's memory was not injected:\n%s", after)
	}
	if strings.Contains(after, "47821") {
		t.Fatalf("the old backend's memory survived the swap:\n%s", after)
	}
}

// The outgoing service owns a writer goroutine holding queued extractions.
// Dropping the pointer would strand them silently, so the swap drains it.
func TestSwapDrainsTheOutgoingService(t *testing.T) {
	svc, err := New("drain").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	old := &countingMemoryService{}
	svc.SetMemoryService(old)
	svc.SetMemoryService(&countingMemoryService{})

	if old.closed.Load() != 1 {
		t.Fatalf("the replaced service was closed %d times, want exactly one drain", old.closed.Load())
	}
}

// nil turns memory off. The run still works; it just stops remembering.
func TestSwapToNilTurnsMemoryOff(t *testing.T) {
	svc, err := New("off").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).WithMemory().Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if !svc.MemoryEnabled() {
		t.Fatal("a service built WithMemory should have memory")
	}
	svc.SetMemoryService(nil)
	if svc.MemoryEnabled() {
		t.Fatal("memory should be off after swapping in nil")
	}
	if _, err := svc.Run(context.Background(), "Work."); err != nil {
		t.Fatalf("a run without memory failed: %v", err)
	}
}

// Nineteen unguarded field reads were fine while the field was written once
// at construction, and a data race the moment it is not. Run with -race.
func TestSwapIsSafeWhileRunsAreInFlight(t *testing.T) {
	svc, err := New("racy").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&scriptedLLM{finishAt: 0}).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = svc.Run(context.Background(), "Work.")
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			select {
			case <-stop:
				return
			default:
				svc.SetMemoryService(&countingMemoryService{})
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// countingMemoryService is a memory backend that remembers nothing and only
// records that it was closed.
type countingMemoryService struct{ closed atomic.Int32 }

func (c *countingMemoryService) Close() error { c.closed.Add(1); return nil }

func (c *countingMemoryService) RetrieveAndInject(context.Context, string, string) (string, []*domain.MemoryWithScore, error) {
	return "", nil, nil
}

func (c *countingMemoryService) RetrieveAndInjectWithLogic(context.Context, string, string) (string, []*domain.MemoryWithScore, string, error) {
	return "", nil, "", nil
}

func (c *countingMemoryService) RetrieveAndInjectWithContext(context.Context, string, domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, error) {
	return "", nil, nil
}

func (c *countingMemoryService) RetrieveAndInjectWithContextAndLogic(context.Context, string, domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, string, error) {
	return "", nil, "", nil
}

func (c *countingMemoryService) StoreIfWorthwhile(context.Context, *domain.MemoryStoreRequest) error {
	return nil
}
func (c *countingMemoryService) Add(context.Context, *domain.Memory) error       { return nil }
func (c *countingMemoryService) Update(context.Context, string, string) error    { return nil }
func (c *countingMemoryService) Delete(context.Context, string) error            { return nil }
func (c *countingMemoryService) Clear(context.Context) error                     { return nil }
func (c *countingMemoryService) Reflect(context.Context, string) (string, error) { return "", nil }
func (c *countingMemoryService) AddMentalModel(context.Context, *domain.MentalModel) error {
	return nil
}
func (c *countingMemoryService) ConfigureBank(context.Context, string, *domain.MemoryBankConfig) error {
	return nil
}
func (c *countingMemoryService) Search(context.Context, string, int) ([]*domain.MemoryWithScore, error) {
	return nil, nil
}
func (c *countingMemoryService) Get(context.Context, string) (*domain.Memory, error) {
	return nil, nil
}
func (c *countingMemoryService) List(context.Context, int, int) ([]*domain.Memory, int, error) {
	return nil, 0, nil
}
